import { useEffect, useRef, useState } from "react";

function readStorage(projectID: string) {
  const result: Record<string, Record<string, string>> = {};
  for (const kind of ["local", "session"] as const) {
    try { result[kind] = JSON.parse(window[kind+"Storage" as "localStorage" | "sessionStorage"].getItem(`atoms-app-storage:${projectID}`) || "{}"); }
    catch { result[kind] = {}; }
  }
  return result;
}

export function ProjectPreview({ projectID, url }: {projectID: string; url: string}) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [storage] = useState(() => readStorage(projectID));
  const src = url + "#atoms-storage=" + encodeURIComponent(JSON.stringify(storage));
  useEffect(() => {
    const root = new URL(url);
    const prefix = root.pathname.replace(/\/$/, "");
    const requests = new Map<number, AbortController>();
    const receive = async (event: MessageEvent) => {
      if (event.source !== frame.current?.contentWindow || event.origin !== "null") return;
      const data = event.data;
      if (!data || typeof data !== "object") return;
      if (data.type === "atoms-preview-storage") {
        if (!["local", "session"].includes(data.kind) || !data.values || typeof data.values !== "object") return;
        const encoded = JSON.stringify(data.values);
        if (encoded.length > 1_048_576 || Object.values(data.values).some(value => typeof value !== "string")) return;
        try { window[data.kind+"Storage" as "localStorage" | "sessionStorage"].setItem(`atoms-app-storage:${projectID}`, encoded); } catch {}
        return;
      }
      if (!Number.isSafeInteger(data.id) || data.id < 1) return;
      if (data.type === "atoms-preview-abort") { requests.get(data.id)?.abort(); return; }
      if (data.type !== "atoms-preview-request" || requests.has(data.id)) return;
      const destination = new URL(data.url, root);
      if (destination.origin !== root.origin || destination.pathname !== prefix && !destination.pathname.startsWith(prefix+"/")) return;
      if (!["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"].includes(data.method) || requests.size >= 64) return;
      const controller = new AbortController();
      requests.set(data.id, controller);
      const timeout = window.setTimeout(() => controller.abort(), 150_000);
      const target = frame.current?.contentWindow;
      try {
        const headers = new Headers(data.headers);
        for (const name of [...headers.keys()]) if (/^(cookie|host|origin|referer|connection|upgrade|forwarded|x-forwarded-.*|sec-.*)$/i.test(name)) headers.delete(name);
        const response = await fetch(destination, {method:data.method, headers, body:data.body, credentials:"include", signal:controller.signal, redirect:"manual"});
        if (response.type === "opaqueredirect") throw new Error("Preview API redirected; navigate using the app's links.");
        const body = await response.arrayBuffer();
        target?.postMessage({type:"atoms-preview-response", id:data.id, status:response.status, statusText:response.statusText, headers:[...response.headers], body, url:response.url}, "*");
      } catch (error) {
        target?.postMessage({type:"atoms-preview-response", id:data.id, error:error instanceof Error ? error.message : "Preview request failed"}, "*");
      } finally { window.clearTimeout(timeout); requests.delete(data.id); }
    };
    window.addEventListener("message", receive);
    return () => { window.removeEventListener("message", receive); for (const request of requests.values()) request.abort(); };
  }, [projectID, url]);
  return <iframe ref={frame} title="项目预览" src={src} sandbox="allow-scripts allow-forms allow-modals allow-downloads" />;
}
