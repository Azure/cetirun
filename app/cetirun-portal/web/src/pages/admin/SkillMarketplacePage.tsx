// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import type { ReactNode } from "react";
import {
  Button,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
} from "@fluentui/react-components";
import { ArrowSync20Regular } from "@fluentui/react-icons";
import type { SkillMarketplaceCatalog, SkillMarketplaceSkill, SkillMarketplaceTeamPublication } from "../../api/skillMarketplace";
import { useSkillMarketplace } from "../../hooks/useSkillMarketplace";
import { formatSkillSize, formatSkillTime } from "../../utils/skillMarketplace";
import { StatusLabel } from "../../components/StatusLabel";

export type PublicationState = "upToDate" | "updating" | "pending";

const PUBLICATION_STATES: Record<PublicationState, { label: string; color: "success" | "informative" | "warning" }> = {
  upToDate: { label: "Up to date", color: "success" },
  updating: { label: "Update pending", color: "informative" },
  pending: { label: "Pending", color: "warning" },
};

/**
 * A Team's publication is pending until a release was published for the Team's current selection. It is updating
 * while that release lacks the repository's version of one of its skills, or a missing skill that the repository has
 * since gained. Otherwise it is up to date. skills are the valid skills of the repository.
 */
export function publicationState(team: SkillMarketplaceTeamPublication, skills: SkillMarketplaceSkill[]): PublicationState {
  if (team.releaseSelectionDigest !== team.selectionDigest) return "pending";
  const repository = new Map(skills.map((skill) => [skill.name, skill.version]));
  const behind = (team.skills ?? []).some((skill) => repository.has(skill.name) && repository.get(skill.name) !== skill.version)
    || (team.missing ?? []).some((name) => repository.has(name));
  return behind ? "updating" : "upToDate";
}

function Heading({ children }: { children: ReactNode }) {
  return <h2 className="portal-section-title" style={{ marginBottom: 8 }}>{children}</h2>;
}

function RepositorySummary({ catalog }: { catalog: SkillMarketplaceCatalog }) {
  const entries: Array<[string, ReactNode]> = [
    ["Repository", <span className="portal-mono">{catalog.repository.url || "—"}</span>],
    ["Ref", catalog.repository.ref || "Default branch"],
    ["Path", catalog.repository.path || "Repository root"],
    ["Commit", catalog.commit ? <span className="portal-mono">{catalog.commit}</span> : "Not synchronized yet"],
    ["Synchronized", formatSkillTime(catalog.syncedAt)],
    ["Checked", formatSkillTime(catalog.checkedAt)],
  ];
  return <dl className="portal-facts">
    {entries.map(([label, value]) => <div key={label}>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>)}
  </dl>;
}

function PublicationDetails({ team }: { team: SkillMarketplaceTeamPublication }) {
  const details = [
    team.retained?.length ? `Retained: ${team.retained.join(", ")}` : "",
    team.missing?.length ? `Missing: ${team.missing.join(", ")}` : "",
  ].filter(Boolean);
  if (details.length === 0 && !team.error) return <>—</>;
  return <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
    {details.map((detail) => <Text key={detail} size={200}>{detail}</Text>)}
    {team.error && <span className="portal-error-text" style={{ fontSize: 12, lineHeight: "16px" }}>Error: {team.error}</span>}
  </div>;
}

function CatalogView({ catalog }: { catalog: SkillMarketplaceCatalog }) {
  const skills = catalog.skills ?? [];
  const invalid = catalog.invalid ?? [];
  const teams = catalog.teams ?? [];
  return <>
    {catalog.error && <MessageBar intent="error">
      <MessageBarBody><MessageBarTitle>The last check of the repository failed.</MessageBarTitle>{catalog.error}</MessageBarBody>
    </MessageBar>}
    {catalog.truncated && <MessageBar intent="warning">
      <MessageBarBody>Some lists were shortened to fit the catalog.</MessageBarBody>
    </MessageBar>}

    <section aria-label="Repository">
      <Heading>Repository</Heading>
      <RepositorySummary catalog={catalog} />
    </section>

    <section aria-label="Skills">
      <Heading>Skills ({skills.length})</Heading>
      {skills.length === 0 ? <Text>The repository has no valid skills.</Text> : <div className="portal-table-scroll"><Table aria-label="Skills in the repository" size="small" style={{ minWidth: 900 }}>
        <TableHeader>
          <TableRow>
            <TableHeaderCell style={{ width: "22%" }}>Name</TableHeaderCell>
            <TableHeaderCell>Description</TableHeaderCell>
            <TableHeaderCell style={{ width: 130 }}>Version</TableHeaderCell>
            <TableHeaderCell style={{ width: 190 }}>Updated</TableHeaderCell>
            <TableHeaderCell style={{ width: 64 }}>Files</TableHeaderCell>
            <TableHeaderCell style={{ width: 88 }}>Size</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {skills.map((skill) => <TableRow key={skill.name}>
            <TableCell>
              <div style={{ display: "flex", flexDirection: "column", overflowWrap: "anywhere" }}>
                <Text weight="semibold">{skill.name}</Text>
                <span className="portal-note" style={{ fontSize: 12, lineHeight: "16px" }}>{skill.path}</span>
              </div>
            </TableCell>
            <TableCell>{skill.description}</TableCell>
            <TableCell><span className="portal-mono">{skill.version}</span></TableCell>
            <TableCell>{formatSkillTime(skill.updatedAt)}</TableCell>
            <TableCell>{skill.files}</TableCell>
            <TableCell>{formatSkillSize(skill.bytes)}</TableCell>
          </TableRow>)}
        </TableBody>
      </Table></div>}
    </section>

    {invalid.length > 0 && <section aria-label="Invalid skills">
      <Heading>Invalid skills ({invalid.length})</Heading>
      <Text size={200} block style={{ marginBottom: 8 }}>Teams cannot choose these skill directories until they are fixed in the repository.</Text>
      <div className="portal-table-scroll"><Table aria-label="Invalid skill directories" size="small">
        <TableHeader>
          <TableRow>
            <TableHeaderCell style={{ width: "34%" }}>Path</TableHeaderCell>
            <TableHeaderCell>Reason</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {invalid.map((problem) => <TableRow key={problem.path}>
            <TableCell><span className="portal-mono">{problem.path}</span></TableCell>
            <TableCell>{problem.reason}</TableCell>
          </TableRow>)}
        </TableBody>
      </Table></div>
    </section>}

    <section aria-label="Team publications">
      <Heading>Team publications</Heading>
      <Text size={200} block style={{ marginBottom: 8 }}>
        Each policy-bound Team's selection is published as a release. Its Agents copy the current release when they start.
      </Text>
      {teams.length === 0 ? <Text>No Team has a publication yet.</Text> : <div className="portal-table-scroll"><Table aria-label="Team publications" size="small" style={{ minWidth: 900 }}>
        <TableHeader>
          <TableRow>
            <TableHeaderCell style={{ width: "16%" }}>Team</TableHeaderCell>
            <TableHeaderCell style={{ width: 190 }}>Release</TableHeaderCell>
            <TableHeaderCell style={{ width: 190 }}>Published</TableHeaderCell>
            <TableHeaderCell style={{ width: 64 }}>Skills</TableHeaderCell>
            <TableHeaderCell style={{ width: 150 }}>State</TableHeaderCell>
            <TableHeaderCell>Details</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {teams.map((team) => {
            const state = PUBLICATION_STATES[publicationState(team, skills)];
            return <TableRow key={team.uid || team.name}>
              <TableCell>{team.name}</TableCell>
              <TableCell>{team.release ? <span className="portal-mono">{team.release}</span> : "—"}</TableCell>
              <TableCell>{formatSkillTime(team.publishedAt)}</TableCell>
              <TableCell>{team.skills?.length ?? 0}</TableCell>
              <TableCell>
                <StatusLabel tone={state.color}>{state.label}</StatusLabel>
              </TableCell>
              <TableCell><PublicationDetails team={team} /></TableCell>
            </TableRow>;
          })}
        </TableBody>
      </Table></div>}
    </section>
  </>;
}

/** The Skill Marketplace: its repository, its skills, and the publication of each Team's selection. */
export function SkillMarketplacePage() {
  const { data, isLoading, isFetching, error, refetch } = useSkillMarketplace();
  const catalog = data?.catalog;
  return (
    <div style={{ maxWidth: 1100, display: "flex", flexDirection: "column", gap: 28 }}>
      <div>
        <div className="portal-page-head">
          <h1 className="portal-page-title portal-page-title--section">Skills</h1>
          <Button icon={<ArrowSync20Regular />} disabled={isFetching} onClick={() => void refetch()}>Refresh</Button>
        </div>
        <p className="portal-page-lede" style={{ marginBottom: 0 }}>
          Teams choose their skills from one Git repository. The Skill Marketplace checks the repository regularly and publishes
          each Team's selection for its Agents.
        </p>
      </div>
      {isLoading && <Spinner label="Loading the Skill Marketplace..." />}
      {error && <MessageBar intent="error"><MessageBarBody>Failed to load: {error.message}</MessageBarBody></MessageBar>}
      {data && !data.enabled && <MessageBar intent="info">
        <MessageBarBody>The Skill Marketplace is not enabled in this installation.</MessageBarBody>
      </MessageBar>}
      {data?.enabled && !catalog && <MessageBar intent="info">
        <MessageBarBody>The Skill Marketplace has not published its catalog yet.</MessageBarBody>
      </MessageBar>}
      {catalog && <CatalogView catalog={catalog} />}
    </div>
  );
}
