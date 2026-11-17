// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { NavLink, Outlet } from "react-router-dom";
import {
  Bot20Regular,
  Box20Regular,
  PeopleTeam20Regular,
  PuzzlePiece20Regular,
  ShieldTask20Regular,
} from "@fluentui/react-icons";
import { useFeatures } from "../../hooks/useFeatures";

export function AdminLayout() {
  const { data: features } = useFeatures();
  return (
    <div className="portal-split">
      <aside className="portal-side">
        <span className="portal-side-heading">Admin</span>
        <div className="portal-side-nav">
          <NavLink to="/admin/teams" className="portal-side-link">
            <PeopleTeam20Regular />
            Teams
          </NavLink>
          <NavLink to="/admin/policies" className="portal-side-link">
            <ShieldTask20Regular />
            Agent policies
          </NavLink>
          <NavLink to="/admin/sandboxes" className="portal-side-link">
            <Box20Regular />
            Sandboxes
          </NavLink>
          {features?.skillMarketplaceEnabled && (
            <NavLink to="/admin/skills" className="portal-side-link">
              <PuzzlePiece20Regular />
              Skills
            </NavLink>
          )}
          <NavLink to="/admin/instances" className="portal-side-link">
            <Bot20Regular />
            Agents
          </NavLink>
        </div>
      </aside>
      <main className="portal-split-main">
        <div className="portal-split-scroll">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
