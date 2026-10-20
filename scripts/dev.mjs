import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(scriptDir, "..");
const webRoot = resolve(repositoryRoot, "web");
const viteEntry = resolve(webRoot, "node_modules", "vite", "bin", "vite.js");

if (!existsSync(viteEntry)) {
  console.error("Frontend dependencies are missing. Run npm ci in the web directory first.");
  process.exit(1);
}

const children = [];
let stopping = false;

function start(label, command, args, cwd, options = {}) {
  const child = spawn(command, args, {
    cwd,
    stdio: "inherit",
    detached: process.platform !== "win32",
    windowsHide: false,
  });
  children.push({ label, child, killTree: options.killTree === true });
  child.once("error", (error) => {
    console.error(label + " failed to start: " + error.message);
    void shutdown(1);
  });
  child.once("close", (code, signal) => {
    if (!stopping) {
      const reason = signal ? "signal " + signal : "exit code " + code;
      console.error(label + " stopped unexpectedly (" + reason + ").");
      void shutdown(code && code !== 0 ? code : 1);
    }
  });
  return child;
}

function stop(entry, force = false) {
  const { child, killTree } = entry;
  if (child.pid === undefined || child.exitCode !== null || child.signalCode !== null) {
    return;
  }

  if (process.platform === "win32" && killTree) {
    const killer = spawn(
      "taskkill.exe",
      ["/PID", String(child.pid), "/T", "/F"],
      { stdio: "ignore", windowsHide: true },
    );
    killer.once("error", () => child.kill("SIGTERM"));
    return;
  }

  const signal = force ? "SIGKILL" : "SIGTERM";
  try {
    if (process.platform !== "win32" && child.pid !== undefined) {
      process.kill(-child.pid, signal);
    } else {
      child.kill(signal);
    }
  } catch (error) {
    if (error.code !== "ESRCH") {
      console.error("Could not stop " + entry.label + ": " + error.message);
    }
  }
}

async function shutdown(exitCode) {
  if (stopping) {
    return;
  }
  stopping = true;
  process.exitCode = exitCode;

  const closed = Promise.all(
    children.map(({ child }) =>
      child.exitCode !== null || child.signalCode !== null
        ? Promise.resolve()
        : new Promise((resolveClose) => child.once("close", resolveClose)),
    ),
  );
  for (const entry of children) {
    stop(entry);
  }

  const forceTimer = setTimeout(() => {
    for (const entry of children) {
      stop(entry, true);
    }
  }, 5000);
  forceTimer.unref();
  await closed;
  clearTimeout(forceTimer);
}

process.once("SIGINT", () => void shutdown(130));
process.once("SIGTERM", () => void shutdown(143));

console.log("Starting MetricsPanel233 development workspace.");
console.log("API: http://127.0.0.1:7333 | UI: http://127.0.0.1:5173");
console.log("Press Ctrl+C to stop both services.");

start(
  "Go API",
  process.platform === "win32" ? "go.exe" : "go",
  ["run", "./cmd/metricspanel", "serve"],
  repositoryRoot,
  { killTree: true },
);
start(
  "Vite",
  process.execPath,
  [viteEntry, "--host", "127.0.0.1"],
  webRoot,
);
