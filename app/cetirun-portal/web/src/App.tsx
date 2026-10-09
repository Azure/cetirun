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
import { PolicyResourceEditPage, PolicyResourceListPage } from "./pages/admin/PolicyResourcePages";
import "./components/Layout.css";

export default function App() {
  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        height: "100dvh",
        overflow: "hidden",
      }}
    >
      <Header />
      <NoticeBanner />
      <div
        style={{
          flex: 1,
          overflow: "hidden",
          display: "flex",
          flexDirection: "column",
        }}
      >
        <Routes>
          <Route path="/" element={<InstanceListPage />} />
          <Route
            path="/instances/:instanceName/:section"
            element={<WorkspacePage />}
          />
          <Route path="/instances/:instanceName" element={<WorkspacePage />} />
          <Route path="/admin" element={<AdminGuard />}>
            <Route element={<AdminLayout />}>
              <Route index element={<Navigate to="teams" replace />} />
              <Route path="teams" element={<TeamListPage />} />
              <Route path="teams/new" element={<TeamEditPage />} />
              <Route path="teams/:name" element={<TeamEditPage />} />
              <Route path="instances" element={<AdminInstanceListPage />} />
              <Route path="agent-policies" element={<PolicyResourceListPage kind="policy" />} />
              <Route path="agent-policies/-/new" element={<PolicyResourceEditPage kind="policy" creating />} />
              <Route path="agent-policies/:name" element={<PolicyResourceEditPage kind="policy" creating={false} />} />
              <Route path="sandboxes" element={<PolicyResourceListPage kind="sandbox" />} />
              <Route path="sandboxes/-/new" element={<PolicyResourceEditPage kind="sandbox" creating />} />
              <Route path="sandboxes/:name" element={<PolicyResourceEditPage kind="sandbox" creating={false} />} />
            </Route>
          </Route>
        </Routes>
      </div>
    </div>
  );
}
