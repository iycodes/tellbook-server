"""Exercise release archives without modifying source or the release directory."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/package-integrations.py'


class PackageIntegrationTests(unittest.TestCase):
    def run_builder(self, output, *arguments):
        return subprocess.run(
            [sys.executable, str(SCRIPT), '--output-dir', str(output), *arguments],
            capture_output=True, text=True, check=False,
        )

    def assert_portable(self, output, api_origin, client_origin):
        for platform in ('chatgpt', 'claude'):
            package = ROOT / 'plugins' / platform
            manifest_name = 'plugin.json' if platform == 'chatgpt' else '.claude-plugin/plugin.json'
            config_name = 'mcp.json' if platform == 'chatgpt' else '.mcp.json'
            source_manifest = json.loads((package / manifest_name).read_text())
            archive = output / f'tellbook-{platform}-{source_manifest["version"]}.zip'
            with zipfile.ZipFile(archive) as bundle:
                self.assertIsNone(bundle.testzip())
                manifest = json.loads(bundle.read(manifest_name))
                config = json.loads(bundle.read(config_name))
                self.assertEqual(manifest['author']['url'], client_origin)
                self.assertEqual(manifest['name'], source_manifest['name'])
                self.assertEqual(manifest['license'], source_manifest['license'])
                self.assertEqual(config['mcpServers']['tellbook']['url'], api_origin + '/mcp/' + platform)
                self.assertNotIn('.app.json', bundle.namelist())
                if platform == 'chatgpt':
                    self.assertEqual(manifest['extensions']['com.openai']['interface']['websiteURL'], client_origin)
                    self.assertNotIn('apps', manifest['extensions']['com.openai'])
                readme = bundle.read('README.md').decode()
                self.assertIn(f'Remote endpoint: `{api_origin}/mcp/{platform}`.', readme)
                self.assertIn(f'Tellbook sign-in and consent: {client_origin}.', readme)
                self.assertEqual(bundle.read('skills/manage-services/SKILL.md'), (package / 'skills/manage-services/SKILL.md').read_bytes())
                for entry in bundle.namelist():
                    if entry.endswith(('.json', '.md')):
                        self.assertNotIn(b'tellbook-beta', bundle.read(entry))

    def test_production_defaults_and_source_preservation(self):
        sources = {
            path: path.read_bytes()
            for platform in ('chatgpt', 'claude')
            for path in (ROOT / 'plugins' / platform).rglob('*')
            if path.is_file()
        }
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            result = self.run_builder(output)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assert_portable(output, 'https://api.tellbook.app', 'https://tellbook.app')
        self.assertEqual(sources, {path: path.read_bytes() for path in sources})

    def test_override_updates_endpoint_listing_and_documentation_together(self):
        # Crossed source origins catch cascading replacements in README URLs.
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            result = self.run_builder(output, '--api-origin', 'https://tellbook.app/',
                                      '--client-origin', 'https://api.tellbook.app/')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assert_portable(output, 'https://tellbook.app', 'https://api.tellbook.app')

    def test_invalid_origins_fail_before_writing_archives(self):
        for option in ('--api-origin', '--client-origin'):
            for value in ('http://example.com', 'https://user:secret@example.com',
                          'https://example.com/path', 'https://example.com?x=1',
                          'https://example.com#fragment', 'https://example.com:bad',
                          'https://[broken', 'https://example.com\\path'):
                with self.subTest(option=option, value=value), tempfile.TemporaryDirectory() as directory:
                    output = Path(directory)
                    result = self.run_builder(output, option, value)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('must be an HTTPS origin', result.stderr)
                    self.assertEqual(list(output.iterdir()), [])

    def test_workspace_mapping_is_explicit_and_does_not_replace_portable_config(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            result = self.run_builder(output, '--chatgpt-server-id', 'plugin_asdk_app_synthetic_registration')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assert_portable(output, 'https://api.tellbook.app', 'https://tellbook.app')
            version = json.loads((ROOT / 'plugins/chatgpt/plugin.json').read_text())['version']
            with zipfile.ZipFile(output / f'tellbook-chatgpt-workspace-{version}.zip') as bundle:
                self.assertNotIn('mcp.json', bundle.namelist())
                manifest = json.loads(bundle.read('plugin.json'))
                self.assertEqual(manifest['extensions']['com.openai']['apps'], './.app.json')
                self.assertEqual(manifest['author']['url'], 'https://tellbook.app')
                self.assertEqual(json.loads(bundle.read('.app.json'))['apps']['tellbook']['id'],
                                 'asdk_app_synthetic_registration')


if __name__ == '__main__':
    unittest.main()
