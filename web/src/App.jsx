import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  Alert,
  Box,
  Button,
  ButtonGroup,
  FormControlLabel,
  Switch,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Container,
  Divider,
  IconButton,
  Snackbar,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import { alpha } from "@mui/material/styles";
import ContentCopyRoundedIcon from "@mui/icons-material/ContentCopyRounded";
import PlayArrowRoundedIcon from "@mui/icons-material/PlayArrowRounded";
import StopRoundedIcon from "@mui/icons-material/StopRounded";
import LogoutRoundedIcon from "@mui/icons-material/LogoutRounded";
import ShieldRoundedIcon from "@mui/icons-material/ShieldRounded";
import ArrowUpwardRoundedIcon from "@mui/icons-material/ArrowUpwardRounded";
import ArrowDownwardRoundedIcon from "@mui/icons-material/ArrowDownwardRounded";

import {
  login as apiLogin,
  logout as apiLogout,
  session as apiSession,
  setTunnel,
  StatusSocket,
  formatBytes,
  formatDuration,
  formatRate,
} from "./api.js";

const PHASES = {
  starting: { label: "启动中", color: "default" },
  connecting: { label: "登录中", color: "warning" },
  ready: { label: "运行中", color: "success" },
  reconnecting: { label: "重连中", color: "warning" },
  stopped: { label: "已停止", color: "error" },
};

export default function App() {
  const [checking, setChecking] = useState(true);
  const [authed, setAuthed] = useState(false);

  useEffect(() => {
    apiSession()
      .then(setAuthed)
      .finally(() => setChecking(false));
  }, []);

  if (checking) {
    return (
      <Box sx={{ minHeight: "100vh", display: "grid", placeItems: "center" }}>
        <CircularProgress size={28} />
      </Box>
    );
  }
  return authed ? (
    <Dashboard onSignOut={() => setAuthed(false)} />
  ) : (
    <LoginPage onSuccess={() => setAuthed(true)} />
  );
}

function LoginPage({ onSuccess }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (event) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    const result = await apiLogin(username.trim(), password);
    setBusy(false);
    if (result.ok) onSuccess();
    else setError(result.message);
  };

  return (
    <Box
      sx={{
        minHeight: "100vh",
        display: "grid",
        placeItems: "center",
        px: 2,
        background:
          "radial-gradient(1200px 600px at 50% -10%, rgba(88,166,255,0.10), transparent 60%)",
      }}
    >
      <Card sx={{ width: "100%", maxWidth: 380 }}>
        <CardContent sx={{ p: 4 }}>
          <Stack spacing={1} sx={{ mb: 3 }} alignItems="center">
            <Box
              sx={{
                width: 44,
                height: 44,
                borderRadius: "12px",
                display: "grid",
                placeItems: "center",
                bgcolor: (t) => alpha(t.palette.primary.main, 0.14),
                color: "primary.main",
              }}
            >
              <ShieldRoundedIcon />
            </Box>
            <Typography variant="h6">TJ Tunnel</Typography>
            <Typography variant="body2" color="text.secondary">
              登录查看隧道状态
            </Typography>
          </Stack>

          <Box component="form" onSubmit={submit}>
            <Stack spacing={2}>
              <TextField
                label="账号"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
                autoFocus
                fullWidth
                size="small"
              />
              <TextField
                label="密码"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                fullWidth
                size="small"
              />
              {error && (
                <Alert severity="error" variant="outlined" sx={{ py: 0.5 }}>
                  {error}
                </Alert>
              )}
              <Button
                type="submit"
                variant="contained"
                size="large"
                disabled={busy || !username || !password}
                startIcon={busy ? <CircularProgress size={16} color="inherit" /> : null}
              >
                {busy ? "登录中…" : "登录"}
              </Button>
            </Stack>
          </Box>
        </CardContent>
      </Card>
    </Box>
  );
}

function Dashboard({ onSignOut }) {
  const [snapshot, setSnapshot] = useState(null);
  const [link, setLink] = useState("open");

  useEffect(() => {
    const socket = new StatusSocket({
      onSnapshot: setSnapshot,
      onState: (state) => {
        setLink(state);
        if (state === "unauthorized") onSignOut();
      },
    });
    socket.connect();
    return () => socket.close();
  }, [onSignOut]);

  const signOut = async () => {
    await apiLogout();
    onSignOut();
  };

  const phase = PHASES[snapshot?.phase] ?? PHASES.starting;
  const online = link === "open";

  return (
    <Container maxWidth="md" sx={{ py: 4 }}>
      <Stack direction="row" alignItems="center" spacing={1.5} sx={{ mb: 3 }} flexWrap="wrap">
        <Typography variant="h6">TJ Tunnel</Typography>
        <Chip
          size="small"
          label={phase.label}
          color={phase.color}
          variant={snapshot?.phase === "ready" ? "filled" : "outlined"}
          sx={{ fontWeight: 600 }}
        />
        <Box sx={{ flex: 1 }} />
        <Tooltip title={online ? "状态流已连接" : "状态流断开，正在重连"}>
          <Chip
            size="small"
            variant="outlined"
            label={online ? "实时连接" : link === "unauthorized" ? "会话已失效" : "重连中…"}
            color={online ? "success" : "default"}
          />
        </Tooltip>
        <Tooltip title="退出登录">
          <IconButton size="small" onClick={signOut}>
            <LogoutRoundedIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      </Stack>

      <Stack spacing={2}>
        <ControlBar snapshot={snapshot} />

        <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
          <Metric title="隧道 IP" value={snapshot?.tunnel_ip || "—"} hint={sessionHint(snapshot)} />
          <Metric
            title="活跃连接"
            value={snapshot?.active_connections ?? 0}
            hint={`${snapshot?.connections ?? 0} 累计连接`}
          />
          <Metric title="运行时长" value={formatDuration(snapshot?.uptime)} hint={snapshot?.version || ""} />
        </Stack>

        <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
          <TrafficCard
            direction="up"
            rate={snapshot?.up_rate}
            total={snapshot?.up_total}
          />
          <TrafficCard
            direction="down"
            rate={snapshot?.down_rate}
            total={snapshot?.down_total}
          />
        </Stack>

        <ConnectionCard snapshot={snapshot} />

        <Card>
          <CardContent>
            <Typography variant="subtitle2" color="text.secondary" gutterBottom>
              运行信息
            </Typography>
            <Stack
              direction={{ xs: "column", sm: "row" }}
              spacing={3}
              divider={<Divider orientation="vertical" flexItem />}
            >
              <Info label="网关" value={snapshot?.gateway} />
              <Info label="认证方式" value={snapshot?.auth_method} />
              <Info
                label="校内 DNS"
                value={snapshot?.dns?.length ? snapshot.dns.join("  ·  ") : undefined}
              />
            </Stack>
          </CardContent>
        </Card>

        {snapshot?.last_error && snapshot.phase !== "ready" && (
          <Alert severity="error" variant="outlined">
            {snapshot.last_error}
            {snapshot.attempts > 0 ? `（已重试 ${snapshot.attempts} 次）` : ""}
          </Alert>
        )}
      </Stack>
    </Container>
  );
}

// ControlBar exposes start/stop and the auto-reconnect switch.
function ControlBar({ snapshot }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const running = snapshot?.running ?? false;
  const auto = snapshot?.auto_reconnect ?? false;
  const ready = snapshot?.phase === "ready";

  const apply = async (payload) => {
    setBusy(true);
    setError("");
    const result = await setTunnel(payload);
    setBusy(false);
    if (!result.ok) setError(result.message);
  };

  return (
    <Card>
      <CardContent sx={{ py: 1.5, "&:last-child": { pb: 1.5 } }}>
        <Stack direction="row" alignItems="center" spacing={2} flexWrap="wrap">
          <ButtonGroup size="small" variant="outlined" disabled={busy}>
            <Button
              startIcon={<PlayArrowRoundedIcon fontSize="small" />}
              color={running ? "inherit" : "success"}
              variant={running ? "outlined" : "contained"}
              disabled={running}
              onClick={() => apply({ running: true })}
            >
              {running ? "已连接" : "连接"}
            </Button>
            <Button
              startIcon={<StopRoundedIcon fontSize="small" />}
              color="error"
              disabled={!running}
              onClick={() => apply({ running: false })}
            >
              断开
            </Button>
          </ButtonGroup>

          <FormControlLabel
            sx={{ ml: 0 }}
            control={
              <Switch
                size="small"
                checked={auto}
                disabled={busy}
                onChange={(event) => apply({ autoReconnect: event.target.checked })}
              />
            }
            label={<Typography variant="body2">自动重连</Typography>}
          />

          <Box sx={{ flex: 1 }} />
          <Typography variant="caption" color="text.secondary">
            {controlHint(running, ready, auto)}
          </Typography>
        </Stack>

        {error && (
          <Alert severity="error" variant="outlined" sx={{ mt: 1.5, py: 0.25 }}>
            {error}
          </Alert>
        )}
      </CardContent>
    </Card>
  );
}

function controlHint(running, ready, auto) {
  if (!running) return "已停止 · 点击「连接」建立隧道";
  if (!ready) return auto ? "正在建立连接，失败会自动重试" : "正在建立连接（自动重连已关闭）";
  return auto ? "隧道在线 · 断线后自动重连" : "隧道在线 · 断线后不会自动重连";
}

function sessionHint(snapshot) {
  if (!snapshot?.session_uptime) return "尚未建立会话";
  return `本次会话 ${formatDuration(snapshot.session_uptime)}`;
}

function Metric({ title, value, hint }) {
  return (
    <Card sx={{ flex: 1 }}>
      <CardContent sx={{ py: 1.75, "&:last-child": { pb: 1.75 } }}>
        <Typography variant="caption" color="text.secondary" sx={{ letterSpacing: 0.6 }}>
          {title}
        </Typography>
        <Typography
          variant="h5"
          sx={{ fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", my: 0.5 }}
          noWrap
        >
          {value}
        </Typography>
        <Typography variant="caption" color="text.secondary" noWrap component="div">
          {hint}
        </Typography>
      </CardContent>
    </Card>
  );
}

function TrafficCard({ direction, rate, total }) {
  const up = direction === "up";
  const color = up ? "success.main" : "primary.main";
  return (
    <Card sx={{ flex: 1 }}>
      <CardContent sx={{ py: 1.75, "&:last-child": { pb: 1.75 } }}>
        <Stack direction="row" spacing={0.5} alignItems="center">
          {up ? (
            <ArrowUpwardRoundedIcon sx={{ fontSize: 15, color }} />
          ) : (
            <ArrowDownwardRoundedIcon sx={{ fontSize: 15, color }} />
          )}
          <Typography variant="caption" color="text.secondary" sx={{ letterSpacing: 0.6 }}>
            {up ? "上行" : "下行"}
          </Typography>
        </Stack>
        <Typography
          variant="h5"
          sx={{ color, fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", my: 0.5 }}
        >
          {formatRate(rate)}
        </Typography>
        <Typography variant="caption" color="text.secondary" component="div">
          共 {formatBytes(total)}
        </Typography>
      </CardContent>
    </Card>
  );
}

// ConnectionCard renders ready-to-copy SOCKS5 settings for client devices.
function ConnectionCard({ snapshot }) {
  const [copied, setCopied] = useState("");

  const host = window.location.hostname;
  const port = (snapshot?.listen || "").split(":").pop() || "1080";
  const user = snapshot?.proxy_user || "";
  const auth = Boolean(snapshot?.proxy_auth);

  const url = useMemo(() => {
    const credentials = auth ? `${user || "<用户名>"}:<密码>@` : "";
    return `socks5h://${credentials}${host}:${port}`;
  }, [auth, user, host, port]);

  const rows = useMemo(() => {
    const list = [
      { label: "代理类型", value: "SOCKS5" },
      { label: "服务器", value: host },
      { label: "端口", value: String(port) },
      { label: "UDP", value: snapshot?.udp_live ? "已启用" : "未启用" },
    ];
    if (auth) {
      list.push({ label: "用户名", value: user || "（未设置）" });
      list.push({ label: "密码", value: "部署时设置的 SOCKS_PASS" });
    } else {
      list.push({ label: "认证", value: "无（任何人均可使用）" });
    }
    return list;
  }, [auth, user, host, port, snapshot?.udp_live]);

  const copy = async (label, text) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // The clipboard API needs a secure context; fall back to a textarea.
      const area = document.createElement("textarea");
      area.value = text;
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      document.execCommand("copy");
      area.remove();
    }
    setCopied(label);
  };

  return (
    <Card>
      <CardContent>
        <Stack direction="row" alignItems="center" sx={{ mb: 1.5 }}>
          <Typography variant="subtitle2" color="text.secondary">
            客户端连接信息
          </Typography>
          <Box sx={{ flex: 1 }} />
          <Button
            size="small"
            startIcon={<ContentCopyRoundedIcon fontSize="small" />}
            onClick={() => copy("链接", url)}
          >
            复制链接
          </Button>
          <Button size="small" onClick={() => copy("全部信息", buildPlainText(rows, url))}>
            复制全部
          </Button>
        </Stack>

        <Box
          sx={{
            p: 1.5,
            borderRadius: 2,
            border: 1,
            borderColor: "divider",
            bgcolor: "background.default",
            fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
            fontSize: 13,
            wordBreak: "break-all",
          }}
        >
          {url}
        </Box>

        <Box
          sx={{
            mt: 1.5,
            display: "grid",
            gap: 1,
            gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" },
          }}
        >
          {rows.map((row) => (
            <Stack
              key={row.label}
              direction="row"
              alignItems="center"
              spacing={1}
              sx={{
                px: 1.25,
                py: 0.75,
                borderRadius: 1.5,
                border: 1,
                borderColor: "divider",
                bgcolor: "background.default",
                minWidth: 0,
              }}
            >
              <Typography variant="caption" color="text.secondary" sx={{ width: 48, flexShrink: 0 }}>
                {row.label}
              </Typography>
              <Typography
                variant="caption"
                noWrap
                sx={{ fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", flex: 1, minWidth: 0 }}
              >
                {row.value}
              </Typography>
              <Tooltip title={`复制${row.label}`}>
                <IconButton size="small" sx={{ p: 0.25 }} onClick={() => copy(row.label, row.value)}>
                  <ContentCopyRoundedIcon sx={{ fontSize: 14 }} />
                </IconButton>
              </Tooltip>
            </Stack>
          ))}
        </Box>

        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1.5 }}>
          {auth
            ? "密码为部署时设置的 SOCKS_PASS，出于安全考虑不在页面显示。客户端请使用 socks5h，让代理解析域名。"
            : "当前未开启代理认证，任何能访问该端口的人都可以使用，公网部署请务必设置 SOCKS_USER / SOCKS_PASS。"}
        </Typography>

        <Snackbar
          open={Boolean(copied)}
          autoHideDuration={1600}
          onClose={() => setCopied("")}
          message={`已复制${copied}`}
          anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
        />
      </CardContent>
    </Card>
  );
}

function buildPlainText(rows, url) {
  return [
    "TJ Tunnel · SOCKS5",
    ...rows.map((row) => `${row.label}: ${row.value}`),
    "",
    `链接: ${url}`,
  ].join("\n");
}

function Info({ label, value }) {
  return (
    <Box sx={{ minWidth: 0 }}>
      <Typography variant="caption" color="text.secondary">
        {label}
      </Typography>
      <Typography variant="body2" sx={{ fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace" }}>
        {value || "—"}
      </Typography>
    </Box>
  );
}
