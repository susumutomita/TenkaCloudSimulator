import React,{useEffect,useRef} from 'react';
import {basicSetup} from 'codemirror';
import {EditorState,Compartment} from '@codemirror/state';
import {EditorView} from '@codemirror/view';
import {python} from '@codemirror/lang-python';
import './python-editor.css';
import {plainCodeInput} from './plain-code-input.mjs';

// Bundled editor/parser only: no worker, extension host, CDN or language server.
export default function PythonEditor({value,onChange,readOnly=false,label}) {
 const node=useRef(null),editor=useRef(null),change=useRef(onChange),settings=useRef(new Compartment()),syncing=useRef(false);
 change.current=onChange;
// Keep native typing/paste plain text: rich contenteditable editing can synthesize
 // inline-style DOM fragments that conflict with the strict console CSP.
 const options=()=>[EditorState.readOnly.of(readOnly),EditorView.editable.of(!readOnly),EditorView.contentAttributes.of({'aria-label':label,contenteditable:readOnly?'false':'plaintext-only'})];
 useEffect(()=>{
  const instance=new EditorView({parent:node.current,state:EditorState.create({doc:value,extensions:[EditorView.domEventHandlers({beforeinput:(event,view)=>plainCodeInput(event,view,EditorView.editable)}),basicSetup,python(),EditorView.lineWrapping,EditorView.cspNonce.of(document.querySelector('meta[name=editor-style-nonce]')?.content||''),settings.current.of(options()),EditorView.updateListener.of(update=>{if(update.docChanged&&!syncing.current)change.current(update.state.doc.toString())})]})});
  editor.current=instance;return()=>{instance.destroy();editor.current=null};
 },[]);
 useEffect(()=>{const instance=editor.current;if(instance&&instance.state.doc.toString()!==value){syncing.current=true;try{instance.dispatch({changes:{from:0,to:instance.state.doc.length,insert:value}})}finally{syncing.current=false}}},[value]);
 useEffect(()=>{editor.current?.dispatch({effects:settings.current.reconfigure(options())})},[readOnly,label]);
 return <div className="lambda-python-editor" ref={node}/>;
}
