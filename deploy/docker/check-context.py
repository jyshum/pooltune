#!/usr/bin/env python3
"""Check Docker's actual build-context filtering using harmless secret sentinels."""
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[2]
paths = [root / '.env.pooltune-image-check',
         root / 'internal/burst/.env.pooltune-image-check',
         root / 'ui/src/.env.pooltune-image-check']
created = []
try:
    for path in paths:
        with path.open('x') as file:
            file.write('POOLTUNE_SYNTHETIC_IMAGE_SENTINEL_DO_NOT_SHIP\n')
        created.append(path)
    with tempfile.TemporaryDirectory(prefix='pooltune-context-') as tmp:
        tmp = Path(tmp)
        recipe = tmp / 'Context.Dockerfile'
        recipe.write_text('FROM scratch\nCOPY . /context/\n')
        output = tmp / 'output'
        subprocess.run(['docker', 'build', '-f', str(recipe), '--output',
                        f'type=local,dest={output}', str(root)], check=True)
        context = output / 'context'
        for path in paths:
            assert not (context / path.relative_to(root)).exists(), 'Secret sentinel entered context'
        for relative in ['go.mod', 'cmd/pooltune-burst/main.go', 'internal/store/schema.sql', 'ui/src/App.tsx']:
            assert (context / relative).is_file(), f'Missing build input: {relative}'
        for relative in ['.git', '.superpowers', 'website', 'docs/evidence', 'node_modules', 'ui/node_modules', 'ui/dist']:
            assert not (context / relative).exists(), f'Unexpected context: {relative}'
        print('PASS: required inputs included; three secret sentinels and private/generated directories excluded.')
finally:
    for path in created:
        path.unlink()
