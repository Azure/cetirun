// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import {
  Button,
  Select,
  Text,
  tokens,
} from "@fluentui/react-components";
import { NavLink } from "react-router-dom";
import { useThemeContext, type ThemeMode } from "../context/ThemeContext";
import { useMe } from "../context/MeContext";
import cetirunLogo from "../assets/cetirun.png";

const THEME_MODES: ThemeMode[] = ["system", "light", "dark"];

function isThemeMode(value: string): value is ThemeMode {
  return (THEME_MODES as string[]).includes(value);
}

/**
 * Build the OAuth2 sign-out redirect URL for the current page.
 *
 * Production: <domain> → oauth2.<domain>/oauth2/sign_out
 * Local dev / non-FQDN: relative /oauth2/sign_out
 */
export function buildSignOutUrl(location: {
  protocol: string;
  host: string;
  pathname: string;
  search: string;
  hash: string;
}): string {
  const { protocol, host, pathname, search, hash } = location;
  const returnTo = `${protocol}//${host}${pathname}${search}${hash}`;

  const hostnameOnly = host.split(":")[0];
  const isLocal =
    hostnameOnly === "localhost" ||
    !hostnameOnly.includes(".") ||
    /^\d{1,3}(\.\d{1,3}){3}$/.test(hostnameOnly) ||
    hostnameOnly.startsWith("[");

  if (isLocal) {
    return `/oauth2/sign_out?rd=${encodeURIComponent(returnTo)}`;
  }

  const oauth2Host = `oauth2.${hostnameOnly}`;
  return `${protocol}//${oauth2Host}/oauth2/sign_out?rd=${encodeURIComponent(returnTo)}`;
}

export function Header() {
  const { themeMode, setThemeMode } = useThemeContext();
  const { me } = useMe();
  const handleSignOut = () => {
    window.location.href = buildSignOutUrl(window.location);
  };

  return (
    <header
      style={{
        borderBottom: `1px solid ${tokens.colorNeutralStroke1}`,
      }}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          padding: "12px 48px",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
          <img src={cetirunLogo} alt="CetiRun" width={48} height={48} />
          <Text size={500} weight="semibold">
            CetiRun Portal
          </Text>
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
          {me?.isAdmin && (
            <NavLink
              to="/admin"
              style={({ isActive }) => ({
                textDecoration: "none",
                padding: "4px 8px",
                borderRadius: "4px",
                fontSize: "14px",
                color: isActive ? tokens.colorBrandForeground1 : tokens.colorNeutralForeground1,
                fontWeight: isActive ? 600 : 400,
              })}
            >
              Admin
            </NavLink>
          )}
          <Select
            size="small"
            aria-label="Theme"
            value={themeMode}
            onChange={(_, data) => {
              if (isThemeMode(data.value)) setThemeMode(data.value);
            }}
          >
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </Select>
          <Button
            appearance="subtle"
            size="small"
            onClick={handleSignOut}
          >
            Sign out
          </Button>
        </div>
      </div>
    </header>
  );
}
