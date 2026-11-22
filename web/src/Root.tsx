import { useEffect, useState } from "react";
import { api, type Me } from "./api";
import { App } from "./App";
import { Login } from "./Login";

/** Shows the sign-in page until the session is valid, then the plan view. */
export function Root() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);

  useEffect(() => {
    api.me().then(setMe, () => setMe(null));
  }, []);

  if (me === undefined) return null;
  if (me === null) return <Login onSignedIn={setMe} />;
  return (
    <App
      me={me}
      onSignOut={async () => {
        await api.logout().catch(() => undefined);
        setMe(null);
      }}
      onSessionExpired={() => setMe(null)}
    />
  );
}
