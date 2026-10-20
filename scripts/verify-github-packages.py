#!/usr/bin/env python3
"""Opt-in real GitHub publish/update/cold-sync check. Creates a new test release.
Usage: scripts/verify-github-packages.py OWNER/REPO VERSION
Uses Atlas's normal GitHub credential resolution. Run from the Atlas repository.
"""
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

repo, version = sys.argv[1:]
binary = str(pathlib.Path('bin/atlas').resolve())
checks = []
with tempfile.TemporaryDirectory(prefix='atlas-github-roundtrip-') as tmp:
    root = pathlib.Path(tmp)
    home, cold_home, project, cold_project, source = [root / p for p in ('home', 'cold-home', 'project', 'cold-project', 'source')]
    for directory in (home, cold_home, project, cold_project):
        directory.mkdir()
    def run(*args, home_dir=home, success=True):
        env = dict(os.environ, ATLAS_HOME=str(home_dir))
        process = subprocess.run([binary, *map(str, args)], env=env, text=True, capture_output=True, timeout=120)
        if (process.returncode == 0) != success:
            raise RuntimeError(f'{args}: {process.returncode}\n{process.stdout}\n{process.stderr}')
        return process.stdout
    run('catalog', 'add', 'test', repo)
    run('init', '--harness', 'pi', '--cwd', project)
    run('package', 'add', '@demo/evidence@1.0.0', '--cwd', project)
    skill = project / '.pi/skills/evidence/SKILL.md'
    first = skill.read_bytes()
    shutil.copytree('examples/packages/research-skill', source)
    manifest_path = source / 'atlas-package.json'
    manifest = json.loads(manifest_path.read_text())
    manifest['version'] = version
    manifest_path.write_text(json.dumps(manifest, indent=2) + '\n')
    with (source / 'skills/evidence/SKILL.md').open('a') as stream:
        stream.write(f'\nRelease verification marker: Atlas {version}.\n')
    published = json.loads(run('package', 'publish', source, '--json'))
    checks.append('publish immutable GitHub release with SHA-256')
    run('package', 'publish', source, success=False)
    checks.append('reject duplicate publication')
    run('package', 'update', '@demo/evidence', '--version', version, '--cwd', project, '--dry-run')
    assert skill.read_bytes() == first
    checks.append('update dry-run leaves native resources unchanged')
    run('package', 'update', '@demo/evidence', '--version', version, '--cwd', project)
    assert version.encode() in skill.read_bytes()
    checks.append('explicit update installs new native skill content')
    (cold_project / '.atlas').mkdir()
    for name in ('project.json', 'packages.json', 'packages.lock.json'):
        shutil.copy2(project / '.atlas' / name, cold_project / '.atlas' / name)
    run('package', 'status', '--cwd', cold_project, home_dir=cold_home, success=False)
    run('package', 'sync', '--locked', '--cwd', cold_project, home_dir=cold_home)
    assert skill.read_bytes() == (cold_project / '.pi/skills/evidence/SKILL.md').read_bytes()
    checks.append('cold client restores exact lock without configured catalog alias')
    installed = skill.read_bytes()
    skill.write_bytes(installed + b'\nUser edit.\n')
    run('package', 'remove', '@demo/evidence', '--cwd', project, success=False)
    assert skill.read_bytes().endswith(b'User edit.\n')
    checks.append('uninstall preserves user edits and reports conflict')
    skill.write_bytes(installed)
    run('package', 'update', '@demo/evidence', '--version', '1.0.0', '--cwd', project)
    assert skill.read_bytes() == first
    checks.append('explicit downgrade restores original release')
    run('package', 'remove', '@demo/evidence', '--cwd', project)
    assert not skill.exists()
    assert (project / '.pi/skills/research/SKILL.md').exists()
    checks.append('remove preserves template resources')
    template_project = root / 'template-project'
    template_project.mkdir()
    run('init', '--harness', 'pi', '--template', '@demo/company-project@1.0.0', '--cwd', template_project)
    assert (template_project / '.pi/skills/evidence/SKILL.md').exists()
    assert (template_project / 'AGENTS.md').exists()
    checks.append('shared project template resolves and installs dependency')
    print(json.dumps({'repository': repo, 'publishedRelease': published['url'], 'version': version, 'passed': checks}, indent=2))
