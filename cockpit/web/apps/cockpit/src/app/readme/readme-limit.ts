/**
 * How many characters are lexed at once: a README of this size or less is drawn in one go, and a larger one is drawn
 * in parts of about this size, the first at once and the rest when asked for, each after a pause for the browser, so
 * no single lex holds the page for long.
 */
export const SYNC_PARSED_CHARACTERS = 64 * 1024

/** The largest README, in characters, that is parsed in the browser; a larger one is shown as plain text. */
export const MAX_PARSED_CHARACTERS = 256 * 1024
