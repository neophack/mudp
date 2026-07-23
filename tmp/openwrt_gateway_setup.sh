#!/bin/sh
# ─────────────────────────────────────────────────────────────────────────────
# OpenWrt(sulinggg/openwrt:x86_64)网关配置脚本
#
# 拓扑(对应 docker-compose.yml):
#   lan_net (172.210.0.0/24, internal:true) ── 无 Docker NAT, 不能直出公网
#     └─ myapp         172.210.0.10  (eth0)
#     └─ openwrt eth0  172.210.0.2   (br-lan, LAN 侧)
#   wan_net (普通 bridge, 有 Docker NAT) ──── 可出公网
#     └─ openwrt eth1  DHCP/静态      (WAN 侧)
#
#   eth0 → br-lan 由 netifd 自动桥接(镜像默认行为)。
#   Compose 里 openwrt 的 networks 字典序: lan_net < wan_net,
#   所以 eth0=lan_net(172.210.0.2), eth1=wan_net。本脚本据此配置。
#
# 镜像默认 LAN IP 是 192.168.123.1, 必须 uci 改成 172.210.0.2,
# 否则 lan_net 上的容器(myapp)无法把 openwrt 当默认网关。
# ─────────────────────────────────────────────────────────────────────────────

set -e

echo "==> 1/7 等待 uci 就绪 …"
for i in $(seq 1 30); do command -v uci >/dev/null 2>&1 && break; sleep 1; done
command -v uci >/dev/null 2>&1 || { echo "uci 未就绪, 退出"; exit 1; }

echo "==> 2/7 配置 LAN (br-lan = 172.210.0.2, 关闭 DHCP)"
uci -q delete network.lan
uci set network.lan=interface
uci set network.lan.device='br-lan'        # eth0 已被 netifd 桥入 br-lan
uci set network.lan.proto='static'
uci set network.lan.ipaddr='172.210.0.2'   # ★ 改掉默认 192.168.123.1
uci set network.lan.netmask='255.255.255.0'
uci set network.lan.gateway='172.210.0.2'  # openwrt 自己就是 LAN 侧网关
uci -q delete network.lan.delegate
uci -q delete network.lan.ip6assign

# 关掉默认的 lan6(若存在), 避免和静态 lan 抢 eth0/br-lan
uci -q delete network.lan6 2>/dev/null || true

echo "==> 3/7 配置 WAN (eth1 = wan_net, 走宿主 docker bridge 出网)"
# wan_net 是普通 bridge, gateway 是 172.x.x.1。用 DHCP 最省事 —— Docker 会
# 给 eth1 分配地址 + 默认路由。静态写死也行, 但需要知道 wan_net 的网段。
uci -q delete network.wan
uci set network.wan=interface
uci set network.wan.device='eth1'
uci set network.wan.proto='dhcp'           # ← wan_net 普通桥, DHCP 拿地址最稳
uci -q delete network.wan.delegate
uci -q delete network.wan.ip6assign
# 删掉镜像默认的 wan6(指向 eth1/dhcp6, 在 bridge 上拿不到 lease 会一直报错)
uci -q delete network.wan6 2>/dev/null || true

echo "==> 4/7 配置 dnsmasq 上游 DNS (镜像默认无上游, 会 REFUSE)"
uci -q delete dhcp.lan
uci set dhcp.lan=dhcp
uci set dhcp.lan.interface='lan'
uci set dhcp.lan.ignore='1'                # lan_net 用静态 IP, 不需要 DHCP

uci -q delete dhcp.@dnsmasq[0].server
uci add_list dhcp.@dnsmasq[0].server='8.8.8.8'
uci add_list dhcp.@dnsmasq[0].server='1.1.1.1'
uci set dhcp.@dnsmasq[0].rebind_protection='0'

echo "==> 5/7 关掉容器内核不支持的硬件 offload (否则 fw4 reload 失败)"
uci set firewall.@defaults[0].flow_offloading='0'
uci set firewall.@defaults[0].flow_offloading_hw='0'
uci -q delete firewall.@defaults[0].fullcone
uci -q delete firewall.@defaults[0].fullcone6

# 清掉镜像默认的匿名 zone / forwarding, 否则 fw4 reload 报 "redefinition of symbol"
while uci -q delete firewall.@zone[0]; do :; done
while uci -q delete firewall.@forwarding[0]; do :; done

uci -q delete firewall.lan
uci set firewall.lan=zone
uci set firewall.lan.name='lan'
uci set firewall.lan.network='lan'
uci set firewall.lan.input='ACCEPT'
uci set firewall.lan.forward='ACCEPT'
uci set firewall.lan.output='ACCEPT'

uci -q delete firewall.wan
uci set firewall.wan=zone
uci set firewall.wan.name='wan'
uci set firewall.wan.network='wan'
uci set firewall.wan.input='REJECT'
uci set firewall.wan.forward='REJECT'
uci set firewall.wan.output='ACCEPT'
uci set firewall.wan.masq='1'              # ★ LAN 流量 NAT 后再出 wan_net

# 只允许 lan -> wan 转发
uci -q delete firewall.fwd_lan_wan
uci set firewall.fwd_lan_wan=forwarding
uci set firewall.fwd_lan_wan.src='lan'
uci set firewall.fwd_lan_wan.dest='wan'

# DROP lan -> RFC1918 / loopback, 防止 myapp 探到宿主/物理网/其它 docker bridge
# (值必须 BARE, 不能带引号, 否则 fw4 视为非法 option 跳过整条规则)
n=0
for cidr in 10.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8; do
  key="drop_rfc1918_$n"
  uci -q delete "firewall.$key"
  uci set "firewall.$key=rule"
  uci set "firewall.$key.src=lan"
  uci set "firewall.$key.dest=wan"
  uci set "firewall.$key.dest_ip=$cidr"
  uci set "firewall.$key.target=DROP"
  n=$((n+1))
done

# 放行 LuCI: 宿主 18080 → 容器 80, 流量从 eth1(wan)进, 需显式放行 tcp/80
uci -q delete firewall.allow_luci
uci set firewall.allow_luci=rule
uci set firewall.allow_luci.name='Allow-LuCI'
uci set firewall.allow_luci.src='wan'
uci set firewall.allow_luci.proto='tcp'
uci set firewall.allow_luci.dest_port='80'
uci set firewall.allow_luci.target='ACCEPT'

echo "==> 6/7 commit 并重载网络/防火墙/dnsmasq"
uci commit

/etc/init.d/network restart 2>/dev/null || reload_config 2>/dev/null || true
sleep 3
fw4 reload 2>/dev/null || fw3 reload 2>/dev/null || /etc/init.d/firewall restart 2>/dev/null || true
/etc/init.d/dnsmasq restart 2>/dev/null || true
/etc/init.d/uhttpd restart 2>/dev/null || true

echo "==> 7/7 fw4 zone-dispatch 兜底 (镜像在容器里 zone 规则常编译不进主链)"
# fw4 用 iifname "br-lan" 编译 zone 分发, 但包物理上从 eth0 进桥,
# 在 forward/input 钩子里 iif=eth0 ≠ br-lan, 匹配不到 → 走 handle_reject 回 RST。
# 改用源网段匹配 + 直接 insert 到 base chain, 跑在 handle_reject 之前。幂等。
nft_del_mudp() {
  for h in $(nft -a list chain "$1" "$2" 2>/dev/null \
             | grep 'comment "mudp-' \
             | grep -oE 'handle [0-9]+' | awk '{print $2}'); do
    nft delete rule "$1" "$2" handle "$h" 2>/dev/null
  done
}
nft_del_mudp inet fw4 input
nft_del_mudp inet fw4 forward
nft insert rule inet fw4 input ip saddr 172.210.0.0/24 accept comment '"mudp-allow-lan-input"'
nft insert rule inet fw4 forward iifname "br-lan" oifname "eth1" accept comment '"mudp-allow-lan-to-wan"'
nft insert rule inet fw4 forward iifname "eth1" oifname "br-lan" ct state established,related accept comment '"mudp-allow-wan-to-lan-est"'
nft insert rule inet fw4 input iifname "eth1" tcp dport 80 accept comment '"mudp-allow-luci-wan"'

# 删 dnsmasq 的 53 端口 DNS 劫持规则 (会导致 DNS REFUSED)
HIJACK_HANDLE=$(nft -a list chain inet dnsmasq prerouting 2>/dev/null \
                | grep -i "DNSMASQ HIJACK" \
                | grep -oE "handle [0-9]+" | awk '{print $2}')
if [ -n "$HIJACK_HANDLE" ]; then
  nft delete rule inet dnsmasq prerouting handle "$HIJACK_HANDLE" 2>/dev/null
  echo "  已删除 dnsmasq DNS 劫持规则 (handle $HIJACK_HANDLE)"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " ✅ 配置完成"
echo "   LAN (br-lan): 172.210.0.2/24   ← myapp 的默认网关"
echo "   WAN (eth1):   DHCP (wan_net)   ← 经宿主 docker bridge 出公网"
echo "   LuCI:         http://<宿主>:18080"
echo "   myapp 验证:   ip route add default via 172.210.0.2 后 ping 外网"
echo "═══════════════════════════════════════════════════════════════"
ip -4 addr show br-lan 2>/dev/null | grep inet || true
ip -4 addr show eth1 2>/dev/null | grep inet || true
ip route show 2>/dev/null | head -5
