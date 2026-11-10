import argparse
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('console_setup',Path(__file__).with_name('console-setup.py'));module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)

class SetupTests(unittest.TestCase):
    def setUp(self):
        operator=mock.patch.object(module.os,'geteuid',return_value=1000,create=True);operator.start();self.addCleanup(operator.stop)
    def identity_args(self,root,local=False):
        return argparse.Namespace(deploy_dir=root,issuer='http://127.0.0.1:8000' if local else 'https://auth.unit.test',mesh_origin='http://127.0.0.1:4180' if local else 'https://mesh.unit.test',sentinel_origin='http://127.0.0.1:4181' if local else 'https://sql.unit.test',local=local,test_mail=local,smtp_from=None,smtp_secret_file=None)
    def args(self,root):
        (root/'compose.yml').write_text('services: {}',encoding='utf-8');(root/'.env').write_text('KEEP_ME=value\nAPP_DOMAIN=old.unit.test\n',encoding='utf-8')
        module.identity.configure(self.identity_args(root))
        for name in ['gateway','admin','sentinel']:(root/name).write_text(name+'_controlled_fixture',encoding='utf-8')
        return argparse.Namespace(deploy_dir=root,identity_dir=None,api_origin='https://api.unit.test',gateway_key_file=root/'gateway',gateway_admin_file=root/'admin',sentinel_bootstrap_file=root/'sentinel',user_id='casdoor-uuid-fixture',tenant_id='tenant_fixture',project_id='project_fixture',tables=['orders'],sql_role='writer',platform_admin=False)
    def test_two_clients_shared_issuer_separate_grants_and_private_keys(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args=self.args(root);result=module.configure(args);self.assertFalse(result['live_login_verified']);self.assertEqual(result['email_delivery'],'unconfigured')
            mesh=json.loads((root/'console-private/mesh/config.json').read_text());sql=json.loads((root/'console-private/sentinel/config.json').read_text())
            self.assertEqual(mesh['oidc_issuer'],sql['oidc_issuer']);self.assertNotEqual(mesh['oidc_client_id'],sql['oidc_client_id']);self.assertNotEqual(mesh['origin'],sql['origin'])
            self.assertNotEqual((root/'console-private/mesh/session_key').read_bytes(),(root/'console-private/sentinel/session_key').read_bytes())
            grants=json.loads((root/'console-private/mesh/members.json').read_text());self.assertFalse(grants[args.user_id]['platform_admin']);self.assertEqual(grants[args.user_id]['issuer'],mesh['oidc_issuer'])
            self.assertIn('KEEP_ME=value',(root/'.env').read_text());self.assertIn('AUTH_DOMAIN=auth.unit.test',(root/'.env').read_text())
            before=(root/'.env').read_bytes()
            with self.assertRaises(ValueError):module.configure(args)
            self.assertEqual(before,(root/'.env').read_bytes())
    def test_invalid_metadata_scope_and_origin_refused_before_writes(self):
        for field,value in [('api_origin','https://mesh.unit.test'),('api_origin','https://api.unit.test/path'),('user_id','email@qq.com'),('tables',['*'])]:
            with self.subTest(field=field),tempfile.TemporaryDirectory() as temporary:
                root=Path(temporary);args=self.args(root);setattr(args,field,value)
                with self.assertRaises(ValueError):module.configure(args)
                self.assertFalse((root/'console-private').exists())
    def test_no_smtp_configuration_generates_no_email_provider_or_known_password(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);result=module.identity.configure(self.identity_args(root));self.assertEqual(result['network_calls'],0);self.assertEqual(result['email_delivery'],'unconfigured')
            seed=json.loads((root/'identity-private/init_data.json').read_text(encoding='utf-8'));self.assertFalse(any(x['category']=='Email' for x in seed['providers']));self.assertGreater(len((root/'identity-private/admin_password').read_text()),32)
            for app in seed['applications']:
                self.assertEqual(app['grantTypes'],['authorization_code']);self.assertEqual(app['tokenFormat'],'JWT-Standard');self.assertEqual(app['expireInHours'],8);self.assertFalse(app['enablePassword'])
                self.assertEqual(app['signinMethods'][0]['rule'],'Email only')
                self.assertTrue(app['enableSigninSession']);self.assertTrue(app['enableAutoSignin'])
                self.assertTrue(any(i['name']=='Login button' and i['visible'] for i in app['signinItems']))
                self.assertTrue(any(i['name']=='Verification code' and i['visible'] for i in app['signinItems']))
                self.assertEqual(next(p['rule'] for p in app['providers'] if p['name']=='console-captcha'),'Always')
            original=(root/'identity-private/init_data.json').read_bytes()
            with self.assertRaises(ValueError):module.identity.configure(self.identity_args(root))
            self.assertEqual(original,(root/'identity-private/init_data.json').read_bytes())
    def test_local_test_mail_is_isolated_and_production_rejects_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args=self.identity_args(root,True);result=module.identity.configure(args);self.assertEqual(result['email_delivery'],'test_sink')
            email=json.loads((root/'identity-private/init_data.json').read_text(encoding='utf-8'))['providers'][0];self.assertEqual(email['host'],'mailpit');self.assertEqual(email['sslMode'],'Disable')
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args=self.identity_args(root);args.test_mail=True
            with self.assertRaises(ValueError):module.identity.configure(args)
            self.assertFalse((root/'identity-private').exists())
    def test_qq_authorization_file_is_used_only_with_tls_and_never_returned(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args=self.identity_args(root);args.smtp_from='sender@qq.com';args.smtp_secret_file=root/'authorization';args.smtp_secret_file.write_text('abcdefghijklmnop')
            result=module.identity.configure(args);self.assertNotIn('abcdefghijklmnop',json.dumps(result));seed=json.loads((root/'identity-private/init_data.json').read_text(encoding='utf-8'));email=next(p for p in seed['providers'] if p['category']=='Email');self.assertEqual(email['host'],'smtp.qq.com');self.assertEqual(email['port'],465);self.assertEqual(email['sslMode'],'Enable');self.assertEqual(email['clientSecret'],'abcdefghijklmnop')
    def test_control_characters_in_origins_are_rejected(self):
        for bad in ['https://auth.unit.test\ninitAdminPassword=bad','https://auth.unit.test/path','http://public.example.net']:
            with self.assertRaises(ValueError):module.identity.origin(bad,True)
    @unittest.skipIf(os.name=='nt','POSIX UID guard is verified in Linux')
    def test_other_uid_is_refused_before_private_writes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args=self.args(root)
            with mock.patch.object(module.os,'geteuid',return_value=1001):
                with self.assertRaisesRegex(ValueError,'root or UID 1000'):module.configure(args)
            self.assertFalse((root/'console-private').exists())

if __name__=='__main__':unittest.main()
