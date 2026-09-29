import { LogOut } from "lucide-preact";
import { useEffect, useState } from "preact/hooks";
import { api, setUnauthorizedHandler } from "./api";
import { JobPage } from "./components/JobPage";
import { Login } from "./components/Login";
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
      </main>
    </div>
  );
}
