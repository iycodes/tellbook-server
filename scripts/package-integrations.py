#!/usr/bin/env python3
"""Bundle reviewed manifests, skills and public assets; no runtime secrets."""
from pathlib import Path
import argparse
import json
import re
import zipfile
from urllib.parse import urlparse

root = Path(__file__).resolve().parents[1] / 'plugins'
parser = argparse.ArgumentParser()
parser.add_argument('--api-origin', default='https://api.tellbook.app')
parser.add_argument('--client-origin', default='https://tellbook.app')
parser.add_argument('--output-dir', type=Path, default=root / 'dist')
parser.add_argument('--chatgpt-server-id', help='Existing workspace registration; produces an additional workspace archive')
args = parser.parse_args()

def https_origin(value, name):
    try:
        origin = urlparse(value)
        if (origin.scheme != 'https' or not origin.hostname or origin.username is not None
                or origin.password is not None or origin.path not in ('', '/')
                or origin.query or origin.fragment
                or any(character.isspace() or ord(character) < 32 or character in '\\?#' for character in value)):
            raise ValueError()
        origin.port  # Validate a supplied port as well as the hostname.
    except ValueError:
        parser.error(f'{name} must be an HTTPS origin')
    return value.removesuffix('/')

api_origin = https_origin(args.api_origin, 'api-origin')
client_origin = https_origin(args.client_origin, 'client-origin')
registered_id = args.chatgpt_server_id
if registered_id:
    registered_id = registered_id.removeprefix('plugin_')
    if not re.fullmatch(r'asdk_app_[A-Za-z0-9][A-Za-z0-9_-]*', registered_id):
        parser.error('chatgpt-server-id must be an asdk_app_ or plugin_asdk_app_ registration ID')
for platform in ('chatgpt', 'claude'):
    package = root / platform
    config = package / ('mcp.json' if platform == 'chatgpt' else '.mcp.json')
    document = json.loads(config.read_text())
    source_api_origin = document['mcpServers']['tellbook']['url'].removesuffix('/mcp/' + platform)
    document['mcpServers']['tellbook']['url'] = api_origin + '/mcp/' + platform
    manifest_path = package / ('plugin.json' if platform == 'chatgpt' else '.claude-plugin/plugin.json')
    manifest = json.loads(manifest_path.read_text())
    source_client_origin = manifest['author']['url'].rstrip('/')
    manifest['author']['url'] = client_origin
    interface = manifest.get('extensions', {}).get('com.openai', {}).get('interface')
    if interface is not None:
        interface['websiteURL'] = client_origin
        for field in ('privacyPolicyURL', 'termsOfServiceURL', 'supportURL'):
            value = interface.get(field)
            if isinstance(value, str) and value.startswith(source_client_origin + '/'):
                interface[field] = client_origin + value[len(source_client_origin):]
    version = manifest.get('version', '')
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?', version):
        raise ValueError(f'Invalid plugin version: {manifest_path}')
    allowed = {'README.md', 'LICENSE', 'plugin.json', 'mcp.json', '.mcp.json', '.claude-plugin', 'skills', 'assets'}
    archive = args.output_dir / f'tellbook-{platform}-{version}.zip'
    archive.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
        for source in sorted(package.rglob('*')):
            relative = source.relative_to(package)
            if source.is_file() and relative.parts[0] in allowed:
                if source.is_symlink() or not source.resolve().is_relative_to(package.resolve()):
                    raise ValueError(f'Package files must remain inside the package: {relative}')
                if source.suffix not in {'.json', '.md', '.png', '.jpg', '.svg'} and source.name != 'LICENSE':
                    raise ValueError(f'Unexpected package file: {relative}')
                if source == config:
                    bundle.writestr(str(relative), json.dumps(document, indent=2) + '\n')
                elif source == manifest_path:
                    bundle.writestr(str(relative), json.dumps(manifest, indent=2) + '\n')
                elif relative == Path('README.md'):
                    replacements = {source_api_origin: api_origin, source_client_origin: client_origin}
                    readme = re.sub('|'.join(re.escape(value) for value in replacements),
                                    lambda match: replacements[match.group()], source.read_text())
                    bundle.writestr(str(relative), readme)
                else:
                    bundle.write(source, relative)
    print(archive)
    if platform == 'chatgpt' and registered_id:
        workspace_archive = archive.with_name(f'tellbook-chatgpt-workspace-{version}.zip')
        manifest['extensions']['com.openai']['apps'] = './.app.json'
        app_mapping = {'apps': {'tellbook': {'id': registered_id, 'required': True}}}
        with zipfile.ZipFile(archive) as portable, zipfile.ZipFile(workspace_archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
            for entry in portable.infolist():
                if entry.filename not in {'plugin.json', 'mcp.json'}:
                    bundle.writestr(entry, portable.read(entry.filename))
            bundle.writestr('plugin.json', json.dumps(manifest, indent=2) + '\n')
            bundle.writestr('.app.json', json.dumps(app_mapping, indent=2) + '\n')
        print(workspace_archive)
