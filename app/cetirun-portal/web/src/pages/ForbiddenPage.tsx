// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

export function ForbiddenPage({ message }: { message?: string }) {
  return (
    <div className="portal-centered">
      <span className="portal-centered-code">403 Forbidden</span>
      <span className="portal-centered-note">
        {message || "You do not have access to this page."}
      </span>
    </div>
  );
}
