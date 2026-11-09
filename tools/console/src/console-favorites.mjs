import {serviceDescriptor} from './console-navigation.mjs';

// Only public service IDs are stored. Favorites are a local-browser preference,
// never a credential, resource cache, session binding, or authorization source.
export const FAVORITES_KEY = 'tenkacloud.console.awsFavorites.v1';
export function normalizeFavorites(value) {
  return Array.isArray(value) ? [...new Set(value.filter(id => typeof id === 'string'
    && serviceDescriptor(id)?.provider === 'aws'))] : [];
}
export function decodeFavorites(value) {
  try { return normalizeFavorites(JSON.parse(value)); } catch { return []; }
}
export function readFavorites(storage) {
  try { return decodeFavorites(storage?.getItem(FAVORITES_KEY)); } catch { return []; }
}
export function writeFavorites(storage, values) {
  try { if (!storage) return false; storage.setItem(FAVORITES_KEY, JSON.stringify(normalizeFavorites(values))); return true; }
  catch { return false; }
}
export function toggleFavorite(values, id) {
  const current = normalizeFavorites(values);
  if (serviceDescriptor(id)?.provider !== 'aws') return current;
  return current.includes(id) ? current.filter(value => value !== id) : [...current, id];
}
