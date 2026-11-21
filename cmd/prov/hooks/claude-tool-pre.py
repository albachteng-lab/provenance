#!/usr/bin/env python3
import json
import os
import sys
import subprocess

hook_input = json.load(sys.stdin)

event = {
    'hook_event_name': 'PreToolUse',
    'session_id': hook_input.get('session_id'),
    'tool_name': hook_input.get('tool_name'),
    'tool_use_id': hook_input.get('tool_use_id'),
    'tool_input': hook_input.get('tool_input'),
    'cwd': hook_input.get('cwd'),
    'permission_mode': hook_input.get('permission_mode'),
}

task_run_id = os.environ.get('AI_PROVENANCE_TASK_RUN_ID', '')
if task_run_id:
    event['task_run_id'] = task_run_id

try:
    subprocess.run(
        ['{{PROV_PATH}}', 'capture-hook', '--json'],
        input=json.dumps(event),
        text=True,
        check=False,
        capture_output=True
    )
except Exception:
    pass

sys.exit(0)
