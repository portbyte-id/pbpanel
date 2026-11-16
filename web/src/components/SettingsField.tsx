import { RotateCcw } from "lucide-react"
import { useId, useState } from "react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import type { SettingKey } from "@/lib/api"
import { cn } from "@/lib/utils"

/**
 * One setting row. The control is chosen by the registry's `type`, so a key
 * added to the Go registry appears here with no change to this file.
 *
 * `error` is the server's rejection of this key. The API reports it as the
 * envelope detail prefixed with `settings[i] (name):`, and the page maps it
 * back to the row it belongs to rather than showing one banner for the form.
 */
export function SettingField({
  setting,
  value,
  error,
  onChange,
}: {
  setting: SettingKey
  value: string
  error?: string
  onChange: (value: string) => void
}) {
  const id = useId()
  const errorId = `${id}-error`

  return (
    <div className="flex flex-col gap-1.5 py-3" data-setting={setting.name}>
      <div className="flex items-center justify-between gap-3">
        <Label htmlFor={id} className="font-mono text-sm">
          {setting.name}
        </Label>
        <div className="flex items-center gap-2">
          {setting.restart_required && (
            <Badge variant="outline" title="A change takes effect after the panel restarts">
              restart required
            </Badge>
          )}
          {!setting.is_default && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              title={`Reset to default: ${setting.default}`}
              aria-label={`Reset ${setting.name} to its default`}
              onClick={() => onChange(setting.default)}
            >
              <RotateCcw className="size-3.5" />
            </Button>
          )}
        </div>
      </div>

      <p className="text-muted-foreground text-xs">{setting.description}</p>

      <SettingControl
        id={id}
        setting={setting}
        value={value}
        invalid={error !== undefined}
        describedBy={error ? errorId : undefined}
        onChange={onChange}
      />

      {error && (
        <p id={errorId} role="alert" className="text-destructive text-xs">
          {error}
        </p>
      )}
    </div>
  )
}

function SettingControl({
  id,
  setting,
  value,
  invalid,
  describedBy,
  onChange,
}: {
  id: string
  setting: SettingKey
  value: string
  invalid: boolean
  describedBy?: string
  onChange: (value: string) => void
}) {
  const shared = {
    id,
    "aria-invalid": invalid,
    "aria-describedby": describedBy,
    "data-setting-input": setting.name,
  }

  switch (setting.type) {
    case "bool":
      return (
        <Select
          {...shared}
          value={value === "true" ? "true" : "false"}
          onChange={(e) => onChange(e.target.value)}
          className="max-w-48"
        >
          <option value="true">Enabled</option>
          <option value="false">Disabled</option>
        </Select>
      )

    case "enum":
      return (
        <Select
          {...shared}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="max-w-72"
        >
          {(setting.allowed ?? []).map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </Select>
      )

    case "int":
    case "port":
      return (
        <Input
          {...shared}
          type="number"
          inputMode="numeric"
          step={1}
          min={setting.type === "port" ? 1 : undefined}
          max={setting.type === "port" ? 65535 : undefined}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="max-w-48"
        />
      )

    case "duration":
      return (
        <div className="flex items-center gap-2">
          <Input
            {...shared}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder="30m"
            spellCheck={false}
            className="max-w-48 font-mono"
          />
          <span className="text-muted-foreground text-xs">e.g. 90s, 30m, 2h</span>
        </div>
      )

    case "path":
      return (
        <Input
          {...shared}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
          className={cn("font-mono")}
        />
      )

    case "string":
    default:
      return (
        <Input
          {...shared}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
        />
      )
  }
}

/** Local draft state for the settings form, keyed by setting name. */
export function useSettingsDraft(initial: Record<string, string>) {
  const [values, setValues] = useState<Record<string, string>>(initial)
  return {
    values,
    set: (name: string, value: string) =>
      setValues((prev) => ({ ...prev, [name]: value })),
    reset: (next: Record<string, string>) => setValues(next),
  }
}
