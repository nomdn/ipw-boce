package main

// agentInstallScript 一键安装脚本；__BOCE_URL__ 在下发时替换为本中心地址。
// 用法：curl -fsSL <boce>/agent/install.sh | sudo bash -s -- --token nat_xxx --node <节点ID>
// 安装 boce-agent（守护）+ lemonipw（节点程序）。节点程序的 WS 接入信息由 agent 在审核通过后
// 从中心拉取并写入 /etc/boce-agent/node.env，再启动 lemonipw；审核未通过前不会启动节点程序。
const agentInstallScript = `#!/usr/bin/env bash
set -euo pipefail
BOCE_URL="__BOCE_URL__"
TOKEN=""; NODE=""; DIR="/opt/boce-agent"; PORT="18080"
while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --node)  NODE="$2"; shift 2 ;;
    --dir)   DIR="$2"; shift 2 ;;
    --port)  PORT="$2"; shift 2 ;;
    --uninstall)
      systemctl disable --now boce-agent lemon-ipw-boce 2>/dev/null || true
      rm -f /etc/systemd/system/boce-agent.service /etc/systemd/system/lemon-ipw-boce.service
      systemctl daemon-reload; rm -rf "$DIR" /etc/boce-agent
      echo "已卸载"; exit 0 ;;
    *) echo "未知参数 $1" >&2; exit 1 ;;
  esac
done
[ "$(id -u)" -eq 0 ] || { echo "请用 root 或 sudo 运行" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "需要 systemd" >&2; exit 1; }
command -v curl >/dev/null || { echo "需要 curl" >&2; exit 1; }
[ -n "$TOKEN" ] && [ -n "$NODE" ] || { echo "缺少 --token 或 --node（请从 1IPW-BOCE「捐赠节点」页复制完整命令）" >&2; exit 1; }

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; armv7l|armv7hl) ARCH=armv7 ;; i?86) ARCH=386 ;;
  *) echo "不支持的架构 $(uname -m)" >&2; exit 1 ;;
esac

echo "==> 校验节点 Token 与审核状态"
CODE=$(curl -s -o /tmp/boce-bootstrap.json -w '%{http_code}' -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{\"nodeId\":\"$NODE\"}" "$BOCE_URL/api/agent/bootstrap" || echo 000)
case "$CODE" in
  200) ;;
  401) echo "节点 Token 无效，请在捐赠页重新复制命令" >&2; exit 1 ;;
  403) echo "该节点还没有通过审核，通过后再运行此命令" >&2; exit 1 ;;
  404) echo "找不到该捐赠节点（节点 ID 与 Token 不属于同一账号）" >&2; exit 1 ;;
  *) echo "无法连接 $BOCE_URL（HTTP $CODE）" >&2; exit 1 ;;
esac
rm -f /tmp/boce-bootstrap.json

echo "==> 下载程序（$ARCH）"
mkdir -p "$DIR" /etc/boce-agent
curl -fsSL -o "$DIR/boce-agent.new" "$BOCE_URL/agent/download/boce-agent-linux-$ARCH"
curl -fsSL -o "$DIR/lemonipw.new" "$BOCE_URL/agent/download/lemonipw-linux-$ARCH"
chmod +x "$DIR/boce-agent.new" "$DIR/lemonipw.new"
mv -f "$DIR/boce-agent.new" "$DIR/boce-agent"; mv -f "$DIR/lemonipw.new" "$DIR/lemonipw"

umask 077
cat > /etc/boce-agent/agent.env <<EOF
BOCE_URL=$BOCE_URL
AGENT_TOKEN=$TOKEN
NODE_ID=$NODE
NODE_SERVICE=lemon-ipw-boce
NODE_ENV_FILE=/etc/boce-agent/node.env
EOF
[ -f /etc/boce-agent/node.env ] || : > /etc/boce-agent/node.env
umask 022

cat > /etc/systemd/system/lemon-ipw-boce.service <<EOF
[Unit]
Description=1IPW-BOCE donated probe node
After=network-online.target
Wants=network-online.target
[Service]
WorkingDirectory=$DIR
Environment=PORTS=$PORT
Environment=IPDB=false
Environment=BIND_ADDRESS=127.0.0.1
Environment=IPW_NODE_API_ENABLED=true
Environment=IPW_ALLOW_ANONYMOUS=1
EnvironmentFile=/etc/boce-agent/node.env
ExecStart=$DIR/lemonipw
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/boce-agent.service <<EOF
[Unit]
Description=1IPW-BOCE node agent
After=network-online.target
Wants=network-online.target
[Service]
EnvironmentFile=/etc/boce-agent/agent.env
ExecStart=$DIR/boce-agent
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now boce-agent
echo ""
echo "安装完成。agent 会拉取接入配置并启动节点程序，几十秒后在捐赠页能看到节点在线。"
echo "  systemctl status boce-agent lemon-ipw-boce"
echo "  journalctl -u boce-agent -f"
echo "卸载：curl -fsSL $BOCE_URL/agent/install.sh | sudo bash -s -- --uninstall"
`
