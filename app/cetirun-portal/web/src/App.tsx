// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Routes, Route, Navigate } from "react-router-dom";
import { Header } from "./components/Header";
import { NoticeBanner } from "./components/NoticeBanner";
import { AdminGuard } from "./components/AdminGuard";
import { InstanceListPage } from "./pages/InstanceListPage";
import { WorkspacePage } from "./pages/WorkspacePage";
import { AdminLayout } from "./pages/admin/AdminLayout";
import { TeamListPage } from "./pages/admin/TeamListPage";
import { TeamEditPage } from "./pages/admin/TeamEditPage";
import { InstanceListPage as AdminInstanceListPage } from "./pages/admin/InstanceListPage";
import { GovernanceResourceEditPage, GovernanceResourceListPage } from "./pages/admin/GovernanceResourcePages";
import { GovernanceActionDetailPage, GovernanceActionsPage } from "./pages/admin/GovernanceActionsPage";
import "./components/Layout.css";

export default function App() {
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh", overflow: "hidden" }}>
      <Header />
      <NoticeBanner />
      <div style={{ flex: 1, overflow: "hidden", display: "flex", flexDirection: "column" }}>
        <Routes>
          <Route path="/" element={<InstanceListPage />} />
          <Route path="/instances/:instanceName/:section" element={<WorkspacePage />} />
          <Route path="/instances/:instanceName" element={<WorkspacePage />} />
          <Route path="/admin" element={<AdminGuard />}>
            <Route element={<AdminLayout />}>
              <Route index element={<Navigate to="teams" replace />} />
              <Route path="teams" element={<TeamListPage />} />
              <Route path="teams/new" element={<TeamEditPage />} />
              <Route path="teams/:name" element={<TeamEditPage />} />
              <Route path="instances" element={<AdminInstanceListPage />} />
              <Route path="agent-policies" element={<GovernanceResourceListPage kind="policy" />} />
              <Route path="agent-policies/new" element={<GovernanceResourceEditPage kind="policy" />} />
              <Route path="agent-policies/:name" element={<GovernanceResourceEditPage kind="policy" />} />
              <Route path="sandboxes" element={<GovernanceResourceListPage kind="sandbox" />} />
              <Route path="sandboxes/new" element={<GovernanceResourceEditPage kind="sandbox" />} />
              <Route path="sandboxes/:name" element={<GovernanceResourceEditPage kind="sandbox" />} />
              <Route path="governance/actions" element={<GovernanceActionsPage />} />
              <Route path="governance/actions/:name" element={<GovernanceActionDetailPage />} />
            </Route>
          </Route>
        </Routes>
      </div>
    </div>
  );
}
