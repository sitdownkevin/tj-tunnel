// Small API client: session cookie login plus a self-healing WebSocket that
// carries status snapshots pushed by the server every second.

export async function login(username, password) {
  const res = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (res.status === 401) return { ok: false, message: "账号或密码错误" };
  if (!res.ok) return { ok: false, message: `登录失败（HTTP ${res.status}）` };
  return { ok: true };
}

export async function logout() {
  await fetch("/api/logout", { method: "POST" });
}

export async function session() {
  try {
    const res = await fetch("/api/session", { cache: "no-store" });
    if (!res.ok) return false;
    const body = await res.json();
    return Boolean(body.authenticated);
  } catch {
    return false;
  }
}

// StatusSocket keeps one connection alive, reconnecting with a short backoff.
export class StatusSocket {
  constructor({ onSnapshot, onState }) {
    this.onSnapshot = onSnapshot;
    this.onState = onState; // "open" | "closed" | "connecting" | "unauthorized"
    this.socket = null;
    this.attempt = 0;
    this.closed = false;
    this.timer = null;
  }

  connect() {
    if (this.closed) return;
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    this.onState?.("connecting");
    const socket = new WebSocket(`${proto}//${location.host}/api/ws`);
    this.socket = socket;

    socket.onopen = () => {
      this.attempt = 0;
      this.onState?.("open");
    };
    socket.onmessage = (event) => {
      try {
        this.onSnapshot(JSON.parse(event.data));
      } catch {
        /* ignore malformed frames */
      }
    };
    socket.onclose = (event) => {
      if (this.closed) return;
      // 1008 = policy violation, i.e. the session expired.
      if (event.code === 1008) {
        this.onState?.("unauthorized");
        this.closed = true;
        return;
      }
      this.onState?.("closed");
      const delay = Math.min(15000, 500 * 2 ** this.attempt++);
      this.timer = setTimeout(() => this.connect(), delay);
    };
    socket.onerror = () => socket.close();
  }

  close() {
    this.closed = true;
    clearTimeout(this.timer);
    this.socket?.close();
  }
}

// setTunnel starts/stops the tunnel and/or toggles auto-reconnect.
export async function setTunnel({ running, autoReconnect }) {
  const body = {};
  if (typeof running === "boolean") body.running = running;
  if (typeof autoReconnect === "boolean") body.auto_reconnect = autoReconnect;
  const res = await fetch("/api/tunnel", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    let message = `操作失败（HTTP ${res.status}）`;
    try {
      const payload = await res.json();
      if (payload?.error) message = payload.error;
    } catch {
      /* keep the default message */
    }
    return { ok: false, message };
  }
  return { ok: true, state: await res.json() };
}

export function formatBytes(n) {
  n = Number(n) || 0;
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i += 1;
  }
  return `${i === 0 ? n.toFixed(0) : n.toFixed(n < 10 ? 2 : 1)} ${units[i]}`;
}

export function formatRate(n) {
  return `${formatBytes(n)}/s`;
}

export function formatDuration(seconds) {
  const total = Math.max(0, Math.floor(Number(seconds) || 0));
  const d = Math.floor(total / 86400);
  const h = Math.floor((total % 86400) / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (d) return `${d} 天 ${h} 小时`;
  if (h) return `${h} 小时 ${m} 分`;
  if (m) return `${m} 分 ${s} 秒`;
  return `${s} 秒`;
}
