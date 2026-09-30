import { useEffect, useState } from "preact/hooks";

/**
 * Path routes (the server answers every non-API path with the app):
 *   /                          all stacks
 *   /stacks/<name>[/<tab>]     one stack; tab is services, compose, env, history or deploys
 *   /jobs/<id>                 a deploy and its log
 *   /audit                     the audit log
 *   /new                       add a stack: adopt a running project or create one
 */
export const TABS = ["services", "compose", "env", "history", "deploys"] as const;
export type Tab = (typeof TABS)[number];

export type Route =
  | { page: "home" }
  | { page: "stack"; name: string; tab: Tab }
  | { page: "job"; id: string }
  | { page: "audit" }
  | { page: "new" };

export function parseRoute(path: string): Route {
  const parts = path.split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "stacks" && parts[1]) {
    const tab = TABS.find((t) => t === parts[2]) ?? "services";
    return { page: "stack", name: parts[1], tab };
  }
  if (parts[0] === "jobs" && parts[1]) return { page: "job", id: parts[1] };
  if (parts[0] === "audit") return { page: "audit" };
  if (parts[0] === "new") return { page: "new" };
  return { page: "home" };
}

export function href(route: Route): string {
  switch (route.page) {
    case "home":
      return "/";
    case "stack":
      return `/stacks/${encodeURIComponent(route.name)}${route.tab === "services" ? "" : `/${route.tab}`}`;
    case "job":
      return `/jobs/${encodeURIComponent(route.id)}`;
    case "audit":
      return "/audit";
    case "new":
      return "/new";
  }
}

const listeners = new Set<() => void>();

export function navigate(to: Route | string, replace = false) {
  const url = typeof to === "string" ? to : href(to);
  if (url === window.location.pathname) return;
  if (replace) history.replaceState(null, "", url);
  else history.pushState(null, "", url);
  window.scrollTo(0, 0);
  listeners.forEach((fn) => fn());
}

/** Lets plain <a href="/…"> links navigate without a page load. */
export function onLinkClick(e: MouseEvent) {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) {
    return;
  }
  const a = (e.target as Element).closest("a");
  if (!a || a.target || a.origin !== window.location.origin || a.pathname.startsWith("/api/")) {
    return;
  }
  e.preventDefault();
  navigate(a.pathname);
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(window.location.pathname));
  useEffect(() => {
    const update = () => setRoute(parseRoute(window.location.pathname));
    listeners.add(update);
    window.addEventListener("popstate", update);
    return () => {
      listeners.delete(update);
      window.removeEventListener("popstate", update);
    };
  }, []);
  return route;
}
