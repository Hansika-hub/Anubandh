import { useId, useState, type FormEvent } from "react";
import { Button } from "./ui";

export interface Field {
  name: string;
  label: string;
  initial?: string;
  type?: "text" | "number";
  options?: { value: string; label: string }[];
  hint?: string;
}

/**
 * A button that opens a small inline form. `onSubmit` receives the field values and returns
 * whether the write succeeded; on success the form closes.
 */
export function InlineForm({
  label,
  fields,
  submitLabel,
  onSubmit,
  busy,
  kind = "secondary",
}: {
  label: string;
  fields: Field[];
  submitLabel?: string;
  onSubmit: (values: Record<string, string>) => Promise<boolean>;
  busy?: boolean;
  kind?: "primary" | "secondary" | "danger";
}) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const id = useId();

  if (!open) {
    return (
      <Button
        kind={kind}
        onClick={() => {
          setValues(Object.fromEntries(fields.map((f) => [f.name, f.initial ?? f.options?.[0]?.value ?? ""])));
          setOpen(true);
        }}
      >
        {label}
      </Button>
    );
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await onSubmit(values)) setOpen(false);
  };
  return (
    <form className="inline-form" onSubmit={submit}>
      {fields.map((f) => (
        <label key={f.name} htmlFor={`${id}-${f.name}`}>
          <span>{f.label}</span>
          {f.options ? (
            <select id={`${id}-${f.name}`} value={values[f.name] ?? ""} onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}>
              {f.options.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          ) : (
            <input
              id={`${id}-${f.name}`}
              type={f.type ?? "text"}
              inputMode={f.type === "number" ? "decimal" : undefined}
              value={values[f.name] ?? ""}
              onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
              required
            />
          )}
          {f.hint && <small>{f.hint}</small>}
        </label>
      ))}
      <div className="inline-actions">
        <Button type="submit" kind={kind === "danger" ? "danger" : "primary"} busy={busy}>
          {submitLabel ?? label}
        </Button>
        <Button kind="ghost" onClick={() => setOpen(false)}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
