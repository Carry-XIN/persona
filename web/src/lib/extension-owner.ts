// Whose extension view is being drawn, for what a graph's key keeps (UI version 10, #9727): the per-key store is
// scoped by the owner, so two extensions using the same key never share what it keeps. The page that knows its
// extension's id (ExtensionSlots) sets it; without one, the page's own path is the owner. A module of its own because
// both the first screen (ExtensionSlots) and the renderer's chunk (views/extensions/ViewBody.tsx) read it, and a
// static value import of ViewBody would put the whole renderer back on the first screen (#1794, #3614).
let owner: () => string = () => (typeof location !== "undefined" ? location.pathname : "");

/** Sets whose extension views draw from here on (the page's own, while its pages are on screen). */
export const setExtensionOwner = (who: () => string) => {
  owner = who;
};

/** Whose view is being drawn: the extension's id, else the page's own path (never a shared ""). */
export const extensionOwner = () => owner();
