# Wait until uci/procd are ready (first boot only).
for i in $(seq 1 30); do command -v uci >/dev/null 2>&1 && break; sleep 1; done
command -v uci >/dev/null 2>&1 || { echo "uci not found; image is not ImmortalWrt?"; exit 0; }

# --- LAN = mesh side. eth0 is the mesh veth (primary Docker endpoint); netifd
# auto-bridges it into br-lan, so we keep device='br-lan'. DHCP off — mesh has
# no other DHCP clients (mudp IPAM assigns their IPs). ---
uci -q delete network.lan
uci set network.lan=interface
uci set network.lan.device='br-lan'
uci set network.lan.proto='static'
uci set network.lan.ipaddr='172.31.252.2'
uci set network.lan.netmask='255.255.252.0'
uci set network.lan.gateway='172.31.252.2'
uci -q delete network.lan.delegate
uci -q delete network.lan.ip6assign

# --- WAN side. eth1 is the WAN veth (secondary Docker endpoint = mudp-wrt-wan).
# Masquerade happens in the firewall zone below. ---
uci -q delete network.wan
uci set network.wan=interface
uci set network.wan.device='eth1'
uci set network.wan.proto='static'
uci set network.wan.ipaddr='172.31.248.2'
uci set network.wan.netmask='255.255.252.0'
uci set network.wan.gateway='172.31.248.1'
uci -q delete network.wan.delegate
uci -q delete network.wan.ip6assign
# Drop the default wan6 interface too — it pointed at eth1/dhcp6 and would fight
# our static wan config (the udhcpc on eth1 never gets a lease on this bridge).
uci -q delete network.wan6

# Disable LAN DHCP — tenant containers get their config from mudp IPAM, not wrt.
uci -q delete dhcp.lan
uci set dhcp.lan=dhcp
uci set dhcp.lan.interface='lan'
uci set dhcp.lan.ignore='1'

# Configure dnsmasq upstream resolvers. ImmortalWrt's dnsmasq installs a NAT
# redirect rule ("DNSMASQ HIJACK" in table inet dnsmasq) that captures every
# UDP/53 packet and hands it to the local dnsmasq. On a fresh image dnsmasq has
# no upstream configured (and the container's /etc/resolv.conf points at Docker's
# 127.0.0.11 embedded resolver, which would loop back), so every DNS query ends
# up REFUSED and tenant containers get "Temporary failure resolving". Giving it
# explicit public resolvers fixes this. uci -q delete + add_list is idempotent so
# repeated applies don't accumulate duplicates. rebind_protection off because some
# upstreams return private-space answers that dnsmasq would otherwise drop.
uci -q delete dhcp.@dnsmasq[0].server
uci add_list dhcp.@dnsmasq[0].server='8.8.8.8'
uci add_list dhcp.@dnsmasq[0].server='1.1.1.1'
uci set dhcp.@dnsmasq[0].rebind_protection='0'

# --- firewall globals: disable offloads the container kernel doesn't support. ---
# The image ships with flow_offloading/hardware offload/fullcone enabled by
# default. Inside a container none of those are available, so fw4 reload aborts
# with "Could not process rule: No such file or directory" on the flowtable
# definition and the whole table inet fw4 never gets generated. Turn them off so
# fw4 can compile cleanly (then our zone dispatch + nft rules land).
uci set firewall.@defaults[0].flow_offloading='0'
uci set firewall.@defaults[0].flow_offloading_hw='0'
uci -q delete firewall.@defaults[0].fullcone
uci -q delete firewall.@defaults[0].fullcone6

# --- firewall zones: lan (mesh) <-> wan. wan masquerade ON. ---
# Wipe the image's default anonymous zones (@zone[0], @zone[1]) first: if they
# coexist with our named lan/wan zones below, fw4 aborts reload with
# "redefinition of symbol 'lan_devices'" and the whole table inet fw4 is never
# generated — which in turn makes our nft insert rules below fail with "No such
# file or directory". Loop until no anonymous zone remains (idempotent).
while uci -q delete firewall.@zone[0]; do :; done
# Also clear any stale named zones from a prior run so we start clean.
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
uci set firewall.wan.masq='1'

# Allow lan -> wan forwarding (the only path LAN traffic may take out).
# Clear any image-default anonymous forwarding first (same redefinition hygiene
# as the zones above) to avoid duplicate lan->wan forward rules.
while uci -q delete firewall.@forwarding[0]; do :; done
uci -q delete firewall.fwd_lan_wan
uci set firewall.fwd_lan_wan=forwarding
uci set firewall.fwd_lan_wan.src='lan'
uci set firewall.fwd_lan_wan.dest='wan'

# DROP lan -> RFC1918 / docker bridges / loopback, so tenants can't reach the
# host, the physical LAN, or the Docker daemon. These rules match traffic
# crossing lan->wan whose destination is a private range.
#
# NOTE: uci values must be BARE here (no quotes). uci set foo.bar='val' would
# store the literal 'val' (including the single quotes), which fw4 then rejects
# as invalid options and skips the whole rule. The double quotes are only for
# shell variable expansion of $key; the value itself is unquoted.
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

# Allow LuCI (tcp/80) inbound on wan — Docker's published port arrives
# via eth1 (wan), which is otherwise REJECT'd by the wan zone.
uci -q delete firewall.allow_luci
uci set firewall.allow_luci=rule
uci set firewall.allow_luci.name='Allow-LuCI'
uci set firewall.allow_luci.src='wan'
uci set firewall.allow_luci.proto='tcp'
uci set firewall.allow_luci.dest_port='80'
uci set firewall.allow_luci.target='ACCEPT'

uci commit
# Reload network + firewall. firewall4 (fw4/nftables) is what this image uses;
# fall back to the legacy fw3 path and the init script. reload_config replays
# any committed UCI changes through netifd/firewall procd services.
reload_config 2>/dev/null
fw4 reload 2>/dev/null || fw3 reload 2>/dev/null || /etc/init.d/firewall restart 2>/dev/null || true

# Restart dnsmasq so the upstream resolver config above takes effect right now
# (uci commit only persists; the running dnsmasq keeps its old upstream until
# restarted, which would leave DNS broken for a window after first apply).
/etc/init.d/dnsmasq restart 2>/dev/null

# [fw4 zone-dispatch workaround] On ImmortalWrt-in-Docker, fw4 compiles the zone
# dispatch rules using iifname "br-lan" — but netifd bridges eth0 into br-lan, so
# at the netfilter input/forward hooks the packet's iif is the physical eth0, NOT
# br-lan. The zone rules therefore never match, and the main input/forward chains
# fall through to handle_reject, which answers every SYN with a TCP RST. Symptoms:
#   - tenant containers get "Connection refused" on every outbound (forward chain)
#   - LuCI is unreachable from the LAN side (input chain)
# Match by source subnet instead of interface name (reliable regardless of the
# netifd bridge), and append directly to the base chains so the dispatch runs
# before handle_reject. Idempotent: nft_del_mudp wipes prior mudp-* rules first.
nft_del_mudp() {
  # $1 = table, $2 = chain. Delete every rule whose comment starts "mudp-".
  for h in $(nft -a list chain "$1" "$2" 2>/dev/null | grep 'comment "mudp-' | grep -oE 'handle [0-9]+' | awk '{print $2}'); do
    nft delete rule "$1" "$2" handle "$h" 2>/dev/null
  done
}
nft_del_mudp inet fw4 input
nft_del_mudp inet fw4 forward
nft insert rule inet fw4 input ip saddr '172.31.252.0/22' accept comment "mudp-allow-lan-input"
nft insert rule inet fw4 forward iifname "br-lan" oifname "eth1" accept comment "mudp-allow-lan-to-wan"
nft insert rule inet fw4 forward iifname "eth1" oifname "br-lan" ct state established,related accept comment "mudp-allow-wan-to-lan-est"
echo "wrt config applied"
