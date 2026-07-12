#!/usr/bin/env bun
import { runCapabilityCommand } from './command';

const result = await runCapabilityCommand(Bun.argv.slice(2));
if (result.stdout) process.stdout.write(result.stdout);
if (result.stderr) process.stderr.write(result.stderr);
process.exitCode = result.exitCode;
