#!/bin/sh
# openwrt 网关完整配置脚本 —— 已验证可用(含 fw4 forward 兜底 + DNS 劫持修复)
# 这是 mudp applyWrtConfig 的增强版,修复了两个坑:
#   坑1: fw4 forward 主链没接 zone 分发 → 补原生 nft accept 规则
#   坑2: dnsmasq 的 DNSMASQ HIJACK 劫持 53 端口 → 删除该规则
#
# 网络拓扑(与 mudp 默认 WRTPolicy 保持一致):
#   LAN (mudp-mesh):    eth0 → br-lan  172.31.252.2/22  (WRT 作为 mesh 默认网关)
#   WAN (mudp-wrt-wan): eth1           172.31.248.2/22  gw 172.31.248.1
#   LuCI 对外端口:       宿主机 18080 → 容器 80

# 等 uci 就绪
for i in $(seq 1 30); do command -v uci >/dev/null 2>&1 && break; sleep 1; done
command -v uci >/dev/null 2>&1 || { echo "uci not found"; exit 1; }

# ========== LAN = mesh/LAN 侧(br-lan, eth0 已被 netifd 桥入) ==========
# mudp-mesh 子网 172.31.252.0/22,WRT 的 LAN IP = 172.31.252.2
# mudp-mesh 上的容器默认网关指向此 IP,流量全部经 WRT 转发出网
uci -q delete network.lan
uci set network.lan=interface
uci set network.lan.device='br-lan'
uci set network.lan.proto='static'
uci set network.lan.ipaddr='172.31.252.2'
uci set network.lan.netmask='255.255.252.0'
uci -q delete network.lan.delegate
uci -q delete network.lan.ip6assign

# ========== WAN = eth1,走宿主 docker bridge 出网 ==========
# mudp-wrt-wan 子网 172.31.248.0/22,桥网关 172.31.248.1
uci -q delete network.wan
uci set network.wan=interface
uci set network.wan.device='eth1'
uci set network.wan.proto='static'
uci set network.wan.ipaddr='172.31.248.2'
uci set network.wan.netmask='255.255.252.0'
uci set network.wan.gateway='172.31.248.1'
uci -q delete network.wan.delegate
uci -q delete network.wan.ip6assign
uci -q delete network.wan6

# 关闭 LAN DHCP(客户端用静态 IP,mudp IPAM 负责分配)
uci -q delete dhcp.lan
uci set dhcp.lan=dhcp
uci set dhcp.lan.interface='lan'
uci set dhcp.lan.ignore='1'

# ========== 【坑2修复】dnsmasq: 配上游 DNS + 关 rebind 保护 ==========
uci -q delete dhcp.@dnsmasq[0].server
uci add_list dhcp.@dnsmasq[0].server='8.8.8.8'
uci add_list dhcp.@dnsmasq[0].server='1.1.1.1'
uci set dhcp.@dnsmasq[0].rebind_protection='0'

# ========== 关闭不兼容的 offload 选项(容器内核不支持,否则 fw4 reload 失败) ==========
uci set firewall.@defaults[0].flow_offloading='0'
uci set firewall.@defaults[0].flow_offloading_hw='0'
uci -q delete firewall.@defaults[0].fullcone
uci -q delete firewall.@defaults[0].fullcone6

# ========== 防火墙 zone ==========
# 清除镜像默认匿名 zone/forwarding,避免 fw4 reload 时 "redefinition of symbol" 错误
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
uci set firewall.wan.masq='1'           # MASQUERADE 开关 → mesh 容器流量 NAT 后出网

# 允许 lan -> wan 转发 → mudp-mesh 上的容器流量全部经 WRT 出网
uci -q delete firewall.fwd_lan_wan
uci set firewall.fwd_lan_wan=forwarding
uci set firewall.fwd_lan_wan.src='lan'
uci set firewall.fwd_lan_wan.dest='wan'

# DROP lan -> RFC1918 / docker bridges / loopback(租户容器不能访问宿主/物理 LAN)
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

# ========== 【LuCI 访问】放行 wan 入站的 80 端口(宿主 18080 → 容器 80) ==========
# Docker 把宿主 18080 的流量从 eth1(wan)灌进来,wan zone 默认 REJECT,
# 必须显式放行 tcp/80,否则 LuCI 不可达。这正是 mudp wrt.go 的 allow_luci 规则。
uci -q delete firewall.allow_luci
uci set firewall.allow_luci=rule
uci set firewall.allow_luci.name='Allow-LuCI'
uci set firewall.allow_luci.src='wan'
uci set firewall.allow_luci.proto='tcp'
uci set firewall.allow_luci.dest_port='80'
uci set firewall.allow_luci.target='ACCEPT'

uci commit
reload_config 2>/dev/null
fw4 reload 2>/dev/null || fw3 reload 2>/dev/null || /etc/init.d/firewall restart 2>/dev/null || true

# 重启 dnsmasq 让上游 DNS 配置立即生效
/etc/init.d/dnsmasq restart 2>/dev/null

# ========== 【坑1修复】fw4 forward/input 主链兜底 ==========
# fw4 的 zone 分发在这个镜像下没正确编译进 forward/input 主链,导致:
#   - 转发包被 forward 链的 handle_reject 回 RST(apt update Connection refused)
#   - 到本机的包被 input 链的 handle_reject 回 RST(LuCI 不可达)
# 注意:不能用 iifname "br-lan" 匹配 —— 包物理上从 eth0 进 netifd 桥,在 forward/input
# 钩子里 iif 是 eth0 不是 br-lan,匹配不到。用源/目的 IP 网段匹配才可靠。
# nft_del_mudp 先清旧规则保证幂等。
nft_del_mudp() {
  for h in $(nft -a list chain "$1" "$2" 2>/dev/null | grep 'comment "mudp-' | grep -oE 'handle [0-9]+' | awk '{print $2}'); do
    nft delete rule "$1" "$2" handle "$h" 2>/dev/null
  done
}
nft_del_mudp inet fw4 input
nft_del_mudp inet fw4 forward
nft insert rule inet fw4 input ip saddr 172.31.252.0/22 accept comment '"mudp-allow-lan-input"' 2>/dev/null
nft insert rule inet fw4 forward iifname "br-lan" oifname "eth1" accept comment '"mudp-allow-lan-to-wan"' 2>/dev/null
nft insert rule inet fw4 forward iifname "eth1" oifname "br-lan" ct state established,related accept comment '"mudp-allow-wan-to-lan-est"' 2>/dev/null

# ========== 【坑2修复】删除 dnsmasq 的 DNSMASQ HIJACK 规则 ==========
# 该规则在独立表 inet dnsmasq 里,会把所有 53/udp 重定向到本地 dnsmasq,导致 DNS 失败。
# 找到含 "DNSMASQ HIJACK" 的规则 handle 并删除。
HIJACK_HANDLE=$(nft -a list chain inet dnsmasq prerouting 2>/dev/null | grep -i "DNSMASQ HIJACK" | grep -oE "handle [0-9]+" | awk '{print $2}')
if [ -n "$HIJACK_HANDLE" ]; then
  nft delete rule inet dnsmasq prerouting handle "$HIJACK_HANDLE" 2>/dev/null
  echo "已删除 dnsmasq DNS 劫持规则 (handle $HIJACK_HANDLE)"
fi

echo "wrt config applied (LAN=172.31.252.2/22, WAN=172.31.248.2/22, LuCI=:18080→:80)"
echo "mudp-mesh 上的容器默认网关 = 172.31.252.2 (WRT br-lan), 流量全部经 WRT 出网"
