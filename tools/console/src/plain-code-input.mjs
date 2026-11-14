// Route ordinary, cancellable text input through CodeMirror's transaction API.
// Native contenteditable insertion may synthesize inline-style DOM fragments;
// composition and replacement inputs retain CodeMirror's own specialized path.
export function plainCodeInput(event,view,editableFacet) {
 if(event.inputType!=='insertText'||!event.cancelable||event.isComposing||view.compositionStarted||typeof event.data!=='string'||view.state.readOnly||!view.state.facet(editableFacet))return false;
 view.dispatch({...view.state.replaceSelection(event.data),userEvent:'input.type',scrollIntoView:true});
 return true;
}
