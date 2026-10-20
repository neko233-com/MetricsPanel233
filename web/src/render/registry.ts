import type { RendererDefinition, RendererKind } from "./types";

const renderers = new Map<string, RendererDefinition>();

export function registerRenderer(definition: RendererDefinition) {
  renderers.set(definition.kind, definition);
}

export function getRenderer(kind: RendererKind | undefined): RendererDefinition | undefined {
  return kind ? renderers.get(kind) : undefined;
}

export function registeredRenderers(): RendererDefinition[] {
  return [...renderers.values()];
}
