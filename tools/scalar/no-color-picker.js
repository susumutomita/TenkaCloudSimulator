// Original simulator adapter: the API reference does not offer CSS color editing.
// CodeMirror accepts an empty extension array, so no color-picker code is needed.
export function colorPicker() {
  return [];
}
export const wrapperClassName = "simulator-color-picker-disabled";
