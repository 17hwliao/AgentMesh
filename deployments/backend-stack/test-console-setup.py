import argparse
import base64
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('console_setup',Path(__file__).with_name('console-setup.py'))
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)

class ConsoleSetupTests(unittest.TestCase):
    def setUp(self):
        # Provisioning tests model an allowed operator. CI's actual UID need not
        # equal the container UID; rejection of other UIDs is tested separately.
        operator=mock.patch.object(module.os,'geteuid',return_value=1000,create=True)
        operator.start();self.addCleanup(operator.stop)

    def args(self,root):
        (root/'compose.yml').write_text('services: {}',encoding='utf-8')
        (root/'.env').write_text('KEEP_ME=value\nAPP_DOMAIN=old.unit.test\n',encoding='utf-8')
        for name,text in [('clerk','sk_live_controlled_fixture'),('gateway','gateway_controlled_fixture'),('admin','admin_controlled_fixture'),('sentinel','sentinel_controlled_fixture')]:
            (root/name).write_text(text,encoding='utf-8')
        return argparse.Namespace(deploy_dir=root,mesh_domain='mesh.unit.test',sentinel_domain='sql.unit.test',api_domain='api.unit.test',
            clerk_issuer='https://clerk.unit.test',clerk_publishable_key='pk_live_'+base64.b64encode(b'clerk.unit.test$').decode(),
            clerk_secret_file=root/'clerk',gateway_key_file=root/'gateway',gateway_admin_file=root/'admin',sentinel_bootstrap_file=root/'sentinel',
            user_id='user_fixture',tenant_id='tenant_fixture',project_id='project_fixture',tables=['orders'],sql_role='writer',platform_admin=False)

    def test_configuration_preserves_backend_env_and_separates_grants(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args=self.args(root);result=module.configure(args)
            self.assertFalse(result['cloud_deployed'])
            mesh=json.loads((root/'console-private/mesh/config.json').read_text(encoding='utf-8'))
            sentinel=json.loads((root/'console-private/sentinel/config.json').read_text(encoding='utf-8'))
            self.assertEqual(mesh['clerk_issuer'],sentinel['clerk_issuer'])
            self.assertNotEqual(mesh['origin'],sentinel['origin'])
            grants=json.loads((root/'console-private/mesh/members.json').read_text(encoding='utf-8'))
            self.assertFalse(grants['user_fixture']['platform_admin'])
            self.assertIn('KEEP_ME=value',(root/'.env').read_text(encoding='utf-8'))
            self.assertIn('APP_DOMAIN=api.unit.test',(root/'.env').read_text(encoding='utf-8'))
            self.assertEqual((root/'console-private/mesh/clerk_secret').read_text(),'sk_live_controlled_fixture')
            before=(root/'.env').read_bytes()
            with self.assertRaises(ValueError):module.configure(args)
            self.assertEqual(before,(root/'.env').read_bytes())

    def test_bad_instance_and_scope_refused_before_writes(self):
        for field,value in [('clerk_issuer','https://other.unit.test'),('mesh_domain','sql.unit.test'),('user_id','email@example.com'),('tables',['*'])]:
            with self.subTest(field=field),tempfile.TemporaryDirectory() as temporary:
                root=Path(temporary);args=self.args(root);setattr(args,field,value)
                with self.assertRaises(ValueError):module.configure(args)
                self.assertFalse((root/'console-private').exists())

    @unittest.skipIf(os.name=='nt','POSIX UID guard is exercised in Linux')
    def test_other_uid_is_refused_before_private_writes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args=self.args(root)
            with mock.patch.object(module.os,'geteuid',return_value=1001):
                with self.assertRaisesRegex(ValueError,'root or UID 1000'):
                    module.configure(args)
            self.assertFalse((root/'console-private').exists())

if __name__=='__main__':unittest.main()
