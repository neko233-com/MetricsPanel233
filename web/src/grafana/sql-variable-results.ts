type VariableFrame = {
  length: number;
  fields: { name: string; values: unknown[] }[];
};

export function sqlVariableResults(frames: VariableFrame[]) {
  const result: { text: string; value: string }[] = [];
  const seen = new Set<string>();
  function add(text: unknown, value: unknown) {
    if (text == null || value == null) return;
    const label = String(text),
      id = String(value);
    if (seen.has(id)) return;
    seen.add(id);
    result.push({ text: label, value: id });
  }
  for (const frame of frames) {
    const label = frame.fields.find((field) => field.name === "__text");
    const value = frame.fields.find((field) => field.name === "__value");
    if (label && value) {
      for (let index = 0; index < frame.length; index++)
        add(label.values[index], value.values[index]);
    } else {
      for (const field of frame.fields)
        for (let index = 0; index < frame.length; index++)
          add(field.values[index], field.values[index]);
    }
  }
  return result;
}
