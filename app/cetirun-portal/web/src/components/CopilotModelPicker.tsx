import { Button, Field, Input, Select, Text } from "@fluentui/react-components";
import { ArrowSync20Regular, Warning20Regular } from "@fluentui/react-icons";
import { isCopilotAuthSupported, useCopilotModelsQuery } from "../hooks/useCopilotAuth";
import type { Instance } from "../types/instance";

export function CopilotModelPicker({
  instance,
  value,
  onChange,
  allowDefault = false,
}: {
  instance?: Instance;
  value: string;
  onChange: (value: string) => void;
  allowDefault?: boolean;
}) {
  const modelsQuery = useCopilotModelsQuery(instance);
  const catalogSupported = isCopilotAuthSupported(instance);
  const models = modelsQuery.data?.models ?? [];
  const known = !value || models.some((model) => model.id === value);

  return (
    <Field
      label="GitHub Copilot model"
      hint={instance ? "Models are loaded from this instance's signed-in GitHub Copilot account." : "A new instance has no signed-in Copilot account yet. Enter a model ID or leave blank for the deployment default."}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
        {instance && (
          <div style={{ display: "flex", gap: 8 }}>
            <Select
              value={models.some((model) => model.id === value) ? value : ""}
              onChange={(event) => onChange(event.target.value)}
              disabled={modelsQuery.isLoading || models.length === 0}
              style={{ flex: 1 }}
            >
              {allowDefault ? <option value="">Use deployment default</option> : <option value="" disabled>Select a live Copilot model</option>}
              {models.map((model) => <option key={model.id} value={model.id}>{model.displayName} ({model.id})</option>)}
            </Select>
            <Button appearance="secondary" icon={<ArrowSync20Regular />} onClick={() => void modelsQuery.refetch()} disabled={modelsQuery.isFetching}>Refresh</Button>
          </div>
        )}
        <Input value={value} onChange={(_, data) => onChange(data.value)} placeholder={allowDefault ? "Leave empty for deployment default, or enter model ID" : "e.g. gpt-5.4"} />
        {instance && modelsQuery.isError && <Text size={200}>Live catalog unavailable. You can still enter a model ID manually.</Text>}
        {catalogSupported && !modelsQuery.isLoading && !modelsQuery.isError && models.length === 0 && <Text size={200}>No models were returned. Check GitHub Copilot sign-in, or enter a model ID manually.</Text>}
        {instance && value && !known && models.length > 0 && (
          <Text size={200} style={{ display: "flex", gap: 4, alignItems: "center" }}><Warning20Regular /> This model is not in the current live catalog. It may be rejected by GitHub Copilot.</Text>
        )}
      </div>
    </Field>
  );
}
