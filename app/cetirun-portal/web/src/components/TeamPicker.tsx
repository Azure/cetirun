import { Field, Select } from "@fluentui/react-components";
import { useTeams } from "../hooks/useTeams";

interface TeamPickerProps {
  value: string;
  onChange: (value: string) => void;
}

export function TeamPicker({ value, onChange }: TeamPickerProps) {
  const { data: teams = [], isLoading } = useTeams();

  return (
    <Field label="Team (optional)">
      <Select
        value={value}
        onChange={(_, d) => onChange(d.value)}
        disabled={isLoading}
      >
        <option value="">(none)</option>
        {teams.map((t) => (
          <option key={t.name} value={t.name}>
            {t.name}
          </option>
        ))}
      </Select>
    </Field>
  );
}
