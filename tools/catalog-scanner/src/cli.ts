#!/usr/bin/env bun

import { runCommand } from './command.ts';

const result = await runCommand(Bun.argv.slice(2));
if (result.stdout.length > 0) process.stdout.write(result.stdout);
if (result.stderr.length > 0) process.stderr.write(result.stderr);
process.exitCode = result.exitCode;
