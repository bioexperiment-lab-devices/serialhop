#!/usr/bin/env node
//
// check-commit-message.js — fail a PR whose squash commit release-please
// cannot parse.
//
// WHY THIS EXISTS
// ---------------
// release-please parses each squash commit with @conventional-commits/parser.
// That parser THROWS on certain bodies — most notably a parenthesis "( ... )"
// that spans a line break — and release-please then silently SKIPS the whole
// commit ("Considering: 0 commits" -> no release PR). A feat/fix PR merges
// clean, yet no release is ever cut. This actually happened (PR #196): its
// body wrapped "_(`-race` ... local box is\nwindows/arm64.)_" across two
// lines, so the 2.2.0 release never fired.
//
// The bad body can only be fixed BEFORE merge — `main` is protected + linear
// history, so a squashed commit message can't be rewritten afterward. This
// guard reproduces the exact parse release-please performs (PR title + body,
// the squash commit git will create) and fails the PR check when it would
// throw, while the author can still edit the description.
//
// It mirrors release-please's own call: `require('@conventional-commits/
// parser').parser(message)` — see release-please's src/commit.js. The parser
// version here is pinned in package.json to the one release-please depends on;
// bump them in lockstep. The PR title's conventional-commit type is validated
// separately by the `pr-title` job (amannn/action-semantic-pull-request); this
// guard covers the body, which that action does not see.
//
// Inputs (env): PR_TITLE (required), PR_BODY (optional).
// Exit 0 = parseable; exit 1 = would silently skip the release.

'use strict';

const { parser } = require('@conventional-commits/parser');

function fail(msg) {
  console.error(`::error::${msg}`);
  process.exit(1);
}

const title = process.env.PR_TITLE;
const body = process.env.PR_BODY || '';

if (title === undefined || title === '') {
  fail('PR_TITLE is not set — pass the PR title via the PR_TITLE env var.');
}

// Reconstruct the squash commit git will create: subject = PR title, body =
// PR description, separated by one blank line. Normalize CRLF (GitHub PR
// bodies use \r\n) to LF, which is what git stores and release-please parses.
const message = (body.trim().length ? `${title}\n\n${body}` : title).replace(/\r\n/g, '\n');

try {
  parser(message);
} catch (err) {
  const at = /at (\d+):(\d+)/.exec(err.message || '');
  let pointer = '';
  if (at) {
    const lineNo = Number(at[1]);
    const offending = message.split('\n')[lineNo - 1];
    if (offending !== undefined) pointer = `\nOffending line ${lineNo}: ${offending}`;
  }
  fail(
    `release-please cannot parse this PR's squash commit, so merging it would ` +
      `silently skip the release ("Considering: 0 commits" -> no release PR).\n` +
      `Parser error: ${err.message}${pointer}\n` +
      `Most common cause: a parenthesis "( ... )" that spans a line break in the ` +
      `PR description. Put the whole parenthetical on ONE line, then edit this PR ` +
      `(this check re-runs on edit).`
  );
}

console.log("OK: release-please can parse this PR's squash commit.");
