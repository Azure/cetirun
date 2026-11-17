// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import {
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
} from "@fluentui/react-components";
import {
  Desktop16Regular,
  History20Regular,
  WeatherMoon16Regular,
  WeatherSunny16Regular,
} from "@fluentui/react-icons";
import { lazy, Suspense, useState, type ReactElement } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { useThemeContext, type ThemeMode } from "../context/ThemeContext";
import { useFeatures } from "../hooks/useFeatures";
import { useMe } from "../context/MeContext";
import cetirunLogo from "../assets/cetirun.png";
import { useChatAccount } from "../chat/ChatAccount";

const HistoryDrawer = lazy(() => import("../chat/HistoryDrawer"));

const THEME_MODES: Array<{ mode: ThemeMode; label: string; icon: ReactElement }> = [
  { mode: "system", label: "System", icon: <Desktop16Regular /> },
  { mode: "light", label: "Light", icon: <WeatherSunny16Regular /> },
  { mode: "dark", label: "Dark", icon: <WeatherMoon16Regular /> },
];

export function buildSignOutUrl(location: {
  protocol: string;
  host: string;
  pathname: string;
  search: string;
  hash: string;
}): string {
  const { protocol, host, pathname, search, hash } = location;
  const returnTo = `${protocol}//${host}${pathname}${search}${hash}`;
  return `/oauth2/sign_out?rd=${encodeURIComponent(returnTo)}`;
}

export function Header() {
  const { themeMode, setThemeMode } = useThemeContext();
  const { me } = useMe();
  const { data: features } = useFeatures();
  const account = useChatAccount();
  const { pathname } = useLocation();
  const onAgents = pathname === "/" || pathname.startsWith("/instances");
  const [historyOpen, setHistoryOpen] = useState(false);
  const [signOutOpen, setSignOutOpen] = useState(false);
  const [clearHistory, setClearHistory] = useState(false);
  const [signOutError, setSignOutError] = useState<string>();
  const finishSignOut = async (clear: boolean) => {
    try {
      await account?.signOut(clear);
      window.location.href = buildSignOutUrl(window.location);
    } catch {
      setSignOutError(
        "Local records could not be cleared. You can stay here or sign out without clearing them.",
      );
    }
  };
  const handleSignOut = () => {
    if (account?.scope) {
      setSignOutOpen(true);
      return;
    }
    window.location.href = buildSignOutUrl(window.location);
  };

  return (
    <header className="portal-topbar">
      <div className="portal-topbar-inner">
        <Link to="/" aria-label="CetiRun Portal home" className="portal-brand">
          <img src={cetirunLogo} alt="CetiRun" width={30} height={30} />
          CetiRun Portal
        </Link>
        {(features?.agentToolsEnabled || me?.isAdmin) && (
          <nav className="portal-nav" aria-label="Main navigation">
            <Link to="/" className="portal-nav-link" aria-current={onAgents ? "page" : undefined}>
              Agents
            </Link>
            {features?.agentToolsEnabled && (
              <NavLink to="/connections" className="portal-nav-link">
                Connections
              </NavLink>
            )}
            {me?.isAdmin && (
              <NavLink to="/admin" className="portal-nav-link">
                Admin
              </NavLink>
            )}
          </nav>
        )}
        <div className="portal-topbar-tools">
          {account?.scope && !account.checking && (
            <Button
              appearance="subtle"
              size="small"
              icon={<History20Regular />}
              onClick={() => setHistoryOpen(true)}
            >
              Local history
            </Button>
          )}
          <div className="portal-theme" role="radiogroup" aria-label="Theme">
            {THEME_MODES.map(({ mode, label, icon }) => (
              <label key={mode} className="portal-theme-option" title={`${label} theme`}>
                <input
                  type="radio"
                  name="portal-theme"
                  aria-label={label}
                  checked={themeMode === mode}
                  onChange={() => setThemeMode(mode)}
                />
                <span>{icon}</span>
              </label>
            ))}
          </div>
          {me?.alias && (
            <span className="portal-account" title={me.email || me.alias}>
              {me.alias}
            </span>
          )}
          <Button appearance="subtle" size="small" onClick={handleSignOut}>
            Sign out
          </Button>
        </div>
      </div>
      {account?.scope && !account.checking && historyOpen && (
        <Suspense fallback={null}>
          <HistoryDrawer
            key={account.scope}
            scope={account.scope}
            open={historyOpen}
            onClose={() => setHistoryOpen(false)}
          />
        </Suspense>
      )}
      <Dialog
        open={signOutOpen}
        onOpenChange={(_, data) => setSignOutOpen(data.open)}
      >
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Sign out of CetiRun?</DialogTitle>
            <DialogContent>
              <p>
                Signing out keeps chat history on this browser. On a shared
                computer, clear your local history before leaving.
              </p>
              <Checkbox
                checked={clearHistory}
                onChange={(_, data) => setClearHistory(data.checked === true)}
                label="Clear this account's local chat history"
              />
              {signOutError && <p role="alert">{signOutError}</p>}
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setSignOutOpen(false)}>Stay here</Button>
              {signOutError && (
                <Button onClick={() => void finishSignOut(true)}>
                  Retry clearing
                </Button>
              )}
              <Button
                appearance="primary"
                onClick={() =>
                  void finishSignOut(signOutError ? false : clearHistory)
                }
              >
                {signOutError ? "Sign out without clearing" : "Sign out"}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </header>
  );
}
