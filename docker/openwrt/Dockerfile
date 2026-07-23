# aucneon/openwrt:x86_64
# 基于 sulinggg/openwrt:x86_64,烧入 mudp docker-compose 双臂路由拓扑的网关配置。
#
# 策略:
#   1. 覆盖 /etc/config/{network,firewall,dhcp,uhttpd} —— 但 network 里 lan/wan
#      interface 不写死物理口,只配 IP/proto/gateway;物理口由运行时探测脚本填入。
#   2. 放 /etc/uci-defaults/ 脚本 —— procd 首次启动跑一次:
#        10-mudp-detect-wanlan      按子网识别 WAN/LAN 物理口,写 uci 后 reload network
#        15-mudp-firewall-workarounds   fw3 zone-dispatch 兜底 + 删 dnsmasq 劫持
#      (编号顺序:10 先配网络,15 再基于配好的接口插防火墙规则。)
#   3. 禁用 br_netfilter(双道防线:modprobe 黑名单 + init 脚本)。双臂拓扑下 openwrt
#      内部仍有 br-lan 桥(LAN 网卡桥进去),br_netfilter 把桥内帧拉进 iptables 的
#      问题同样存在,故保留。
#   4. 设 CMD /sbin/init —— sulinggg 基础镜像无 ENTRYPOINT/CMD,procd 需显式启动。
#
# 双臂路由拓扑(对应 docker-compose.yml):
#   openwrt 挂两张独立 docker network,无 VLAN、无 tag:
#     wan_net (10.210.2.0/24, 普通 bridge, 能出公网)     → openwrt 的 WAN 口
#     lan_net (10.210.1.0/24, Internal:true, 不 NAT)     → openwrt 的 LAN 口 (br-lan=10.210.1.2)
#   业务容器只挂 lan_net,docker 自动把默认网关设为 10.210.1.2,零手动配置。
#   WAN/LAN 物理口(eth0/eth1 顺序 docker 不保证)由 10-mudp-detect-wanlan 按子网探测绑定。

FROM sulinggg/openwrt:x86_64

LABEL org.opencontainers.image.authors="aucneon"
LABEL org.opencontainers.image.description="ImmortalWrt dual-arm gateway for mudp docker-compose topology (wan_net=10.210.2.0/24, lan_net=10.210.1.0/24, auto-detected eth ports)"
LABEL mudp.topology="dual-arm wan_net=10.210.2.0/24 lan_net(internal)=10.210.1.0/24 gw=10.210.1.2"

# 覆盖配置文件。COPY 默认会以 root:root + 0644 写入,与 OpenWrt 配置文件
# 权限一致(/etc/config/* 在镜像里就是 0644 root:root)。
COPY config/network   /etc/config/network
COPY config/firewall  /etc/config/firewall
COPY config/dhcp      /etc/config/dhcp
COPY config/uhttpd    /etc/config/uhttpd

# uci-defaults 脚本:procd 首次启动执行一次,成功后自动删除。必须可执行。
#   10-mudp-detect-wanlan         按子网探测 WAN/LAN 物理口,写 uci 后 reload network
#   15-mudp-firewall-workarounds  fw3 zone-dispatch 兜底 + 删 dnsmasq 劫持(依赖 10 配好的口)
COPY uci-defaults/10-mudp-detect-wanlan         /etc/uci-defaults/10-mudp-detect-wanlan
COPY uci-defaults/15-mudp-firewall-workarounds  /etc/uci-defaults/15-mudp-firewall-workarounds
RUN chmod +x /etc/uci-defaults/10-mudp-detect-wanlan \
 && chmod +x /etc/uci-defaults/15-mudp-firewall-workarounds

# ─── 方案B修复:禁用 br_netfilter ──────────────────────────────────────────
# 双臂拓扑下 openwrt 内部仍有 br-lan 桥(LAN 网卡被 netifd 桥进去),问题与单臂一致:
# br_netfilter(bridge-nf-call-iptables=1)把容器内 br-lan 桥的二层帧拉进 iptables,
# 导致经 openwrt 三层转发的包因 iifname 不匹配(物理口 vs br-lan)而进不了 FORWARD 链 →
# LAN 上的容器(myapp)ping 网关通,但 ping 公网全部超时。卸载后转发恢复正常。
# 两道防线:modprobe 黑名单(防自动加载)+ init 脚本(每次 boot 兜底卸载)。
COPY modprobe/blacklist-br_netfilter.conf /etc/modprobe.d/mudp-blacklist-br_netfilter.conf
COPY init/mudp-no-brnf                    /etc/init.d/mudp-no-brnf
RUN chmod +x /etc/init.d/mudp-no-brnf \
 && mkdir -p /etc/rc.d \
 && ln -sf ../init.d/mudp-no-brnf /etc/rc.d/S95mudp-no-brnf

# sulinggg 基础镜像无 ENTRYPOINT/CMD —— 显式设 procd 为 init。
# compose 里可省略 command;若仍写 command: /sbin/init 也无害(等价覆盖)。
CMD ["/sbin/init"]
