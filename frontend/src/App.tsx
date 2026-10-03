import { ArrowLeft, LogOut } from "lucide-preact";
import { useEffect, useState } from "preact/hooks";
import { api, setUnauthorizedHandler } from "./api";
import { settings } from "./lib";
import { AddStackPage } from "./components/AddStackPage";
import { AuditPage } from "./components/AuditPage";
import { JobPage } from "./components/JobPage";
import { Login } from "./components/Login";
import { ReleasesPage } from "./components/ReleasesPage";
import { StackPage } from "./components/StackPage";
import { StacksPage } from "./components/StacksPage";
import { onLinkClick, useRoute } from "./router";
import type { Session } from "./types";

export function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    setUnauthorizedHandler(() => setSession((s) => s && { ...s, authenticated: false }));
    api
      .session()
      .then(setSession)
      .catch((e: Error) => setError(e.message));
  }, []);

  if (error) return <div class="boot">Can't reach Hoist: {error}</div>;
  if (!session) return <div class="boot" />;
  if (!session.authenticated) return <Login onDone={setSession} />;
  settings.conventional = session.conventional;
  return (
    <Shell session={session} onSignOut={() => setSession({ ...session, authenticated: false })} />
  );
}

function Shell(props: { session: Session; onSignOut: () => void }) {
  const route = useRoute();
  const signOut = async () => {
    await api.logout().catch(() => {});
    props.onSignOut();
  };
  return (
    <div class="shell" onClick={onLinkClick}>
      <header class="topbar">
        {props.session.foyer_url && (
          <a class="home-link" href={props.session.foyer_url} title="Back to Foyer">
            <ArrowLeft size={14} />
            <span class="home-link-text">Foyer</span>
          </a>
        )}
        <a class="brand" href="/">
          <span class="brand-mark" aria-hidden="true" />
          Hoist
        </a>
        {props.session.read_only && (
          <span
            class="chip chip-warn"
            title="HOIST_READ_ONLY is set: nothing can be saved or deployed"
          >
            Read-only
          </span>
        )}
        <span class="spacer" />
        <nav class="topnav" aria-label="Pages">
          <a class={route.page === "audit" || route.page === "releases" ? "" : "active"} href="/">
            Stacks
          </a>
          <a class={route.page === "releases" ? "active" : ""} href="/releases">
            Releases
          </a>
          <a class={route.page === "audit" ? "active" : ""} href="/audit">
            Activity
          </a>
        </nav>
        <button class="icon-btn" onClick={signOut} title="Sign out" aria-label="Sign out">
          <LogOut size={16} />
        </button>
      </header>
      <main>
        {route.page === "home" && <StacksPage readOnly={props.session.read_only} />}
        {route.page === "stack" && (
          <StackPage
            key={route.name}
            name={route.name}
            tab={route.tab}
            readOnly={props.session.read_only}
          />
        )}
        {route.page === "job" && <JobPage key={route.id} id={route.id} />}
        {route.page === "audit" && <AuditPage />}
        {route.page === "releases" && <ReleasesPage readOnly={props.session.read_only} />}
        {route.page === "new" && <AddStackPage readOnly={props.session.read_only} />}
      </main>
    </div>
  );
}
