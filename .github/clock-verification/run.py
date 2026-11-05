import json
from pathlib import Path
import subprocess
import sys
import tempfile
from uuid import uuid4

base, candidate = sys.argv[1:]
evidence = Path('evidence')
evidence.mkdir(exist_ok=True)
context = Path(__file__).resolve().parent
results = {}
with tempfile.TemporaryDirectory(prefix='k6-clock-proof-') as scratch:
    for name, revision, expected in [('base', base, 'RED'), ('candidate', candidate, 'GREEN')]:
        source = Path(scratch) / name
        tag = 'k6-clock-proof:' + uuid4().hex
        subprocess.run(['git', 'worktree', 'add', '--detach', str(source), revision], check=True)
        try:
            build = subprocess.run(['docker', 'build', '--build-context', f'k6src={source}', '-t', tag, str(context)],
                                   text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            (evidence / f'{name}-build.log').write_text(build.stdout)
            if build.returncode:
                print(build.stdout)
                raise SystemExit(build.returncode)
            run = subprocess.run(['docker', 'run', '--rm', '--network', 'none', '--cap-drop', 'ALL',
                                  '--security-opt', 'no-new-privileges', tag],
                                 text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            (evidence / f'{name}-run.log').write_text(run.stdout)
            print(run.stdout)
            verdict = next(json.loads(line) for line in run.stdout.splitlines() if line.startswith('{"verdict":'))
            results[name] = {'revision': revision, 'exit_code': run.returncode, 'result': verdict}
            (evidence / 'clock-proof.json').write_text(json.dumps(results, indent=2) + '\n')
            expected_exit = 1 if expected == 'RED' else 0
            if run.returncode != expected_exit or verdict['verdict'] != expected:
                raise SystemExit(f'{name} expected {expected} and exit {expected_exit}')
        finally:
            subprocess.run(['docker', 'image', 'rm', tag], check=False)
            subprocess.run(['git', 'worktree', 'remove', str(source)], check=True)
