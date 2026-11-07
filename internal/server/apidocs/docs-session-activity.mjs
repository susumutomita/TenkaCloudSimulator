// SAME shared browser UX classifier as console. Server checks remain authority.
// Browser URL uses the existing public console asset mount; no code copy.
import {createSessionActivity,sessionRenewalMessage} from '../console/session-activity.mjs';
export function createDocsActivity(create=createSessionActivity,environment){
 const activity=create(environment);
 return Object.freeze({beforeExplicitSend:()=>activity.beforeExplicitAction(true),message:sessionRenewalMessage,dispose:()=>activity.dispose()});
}
export const docsSession=createDocsActivity();
globalThis.SimulatorDocsSessionActivity=docsSession;
