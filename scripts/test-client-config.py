"""Real local HTTP onboarding checks, with no model endpoint or credentials."""
from pathlib import Path
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
from datetime import datetime,timedelta,timezone
import importlib.util,json,tempfile,threading,unittest,shutil,subprocess
spec=importlib.util.spec_from_file_location('profiles',Path(__file__).with_name('client-config.py'));m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_GET(self):
        self.server.paths.append(self.path)
        if self.headers.get('Authorization')!='Bearer fixture-private-gateway-key':self.send_error(401);return
        if self.path not in self.server.responses:self.send_error(404);return
        raw=json.dumps(self.server.responses[self.path]).encode();self.send_response(200);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
    def do_POST(self):raise AssertionError('paid/mutating request attempted')
class Profiles(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name);self.key=self.root/'key';self.key.write_text('fixture-private-gateway-key')
        self.server=ThreadingHTTPServer(('127.0.0.1',0),Handler);self.server.paths=[]
        self.server.responses={'/v1/models':{'data':[{'id':'domestic-chat','capabilities':{'tools':True,'vision':True}}]},'/v1/billing/balance':{'currency':'CNY','scale':1000000000,'available_nano':1000000000},'/v1/billing/prices':{'catalog':{'currency':'CNY','expires_at':(datetime.now(timezone.utc)+timedelta(days=1)).isoformat(),'prices':[{'model':'domestic-chat','max_input_tokens':65536,'input_nano_per_million':800000000,'output_nano_per_million':2700000000}]}},'/readyz':{'status':'ready'}}
        self.thread=threading.Thread(target=self.server.serve_forever);self.thread.start();self.url='http://127.0.0.1:'+str(self.server.server_port)
    def tearDown(self):self.server.shutdown();self.server.server_close();self.thread.join();self.temp.cleanup()
    def run_generate(self):return m.generate(self.url+'/v1','domestic-chat',self.key,self.root/'profiles')
    def test_profiles_use_gateway_alias_env_keys_and_distinct_base_paths(self):
        result=self.run_generate();self.assertEqual(result['paid_calls'],0)
        oc=json.loads((self.root/'profiles/opencode.json').read_text());cc=json.loads((self.root/'profiles/claude-settings.json').read_text())
        self.assertEqual(oc['provider']['agentmesh']['options'],{'baseURL':self.url+'/v1','apiKey':'{env:AGENTMESH_API_KEY}'})
        self.assertEqual(cc['env']['ANTHROPIC_BASE_URL'],self.url)
        for p in (self.root/'profiles').iterdir():self.assertNotIn(self.key.read_text(),p.read_text())
        self.assertEqual(self.server.paths,['/v1/models','/v1/billing/balance','/v1/billing/prices','/readyz'])
        with self.assertRaises(ValueError):self.run_generate()
    def test_unfunded_wallet_does_not_emit_apparently_usable_profiles(self):
        self.server.responses['/v1/billing/balance']['available_nano']=1
        with self.assertRaisesRegex(ValueError,'hold'):self.run_generate()
        self.assertFalse((self.root/'profiles').exists())
    def test_missing_capability_rejected_without_other_requests(self):
        self.server.responses['/v1/models']['data'][0]['capabilities']['tools']=False
        with self.assertRaisesRegex(ValueError,'tool'):self.run_generate()
        self.assertEqual(self.server.paths,['/v1/models'])
    def test_expired_prices_and_not_ready_rejected(self):
        catalog=self.server.responses['/v1/billing/prices']['catalog'];future=catalog['expires_at'];catalog['expires_at']='2020-01-01T00:00:00Z'
        with self.assertRaisesRegex(ValueError,'expired'):self.run_generate()
        catalog['expires_at']=future;self.server.responses['/readyz']['status']='blocked'
        with self.assertRaisesRegex(ValueError,'ready'):self.run_generate()
    def test_unsafe_url_rejected_before_credentials_read(self):
        for url in ['http://remote.example/v1','https://name:key@example.com','https://example.com/?token=x','file:///key']:
            with self.assertRaises(ValueError):m.endpoint(url)
    @unittest.skipUnless(shutil.which('pwsh'),'PowerShell launcher contract requires pwsh')
    def test_launchers_forward_native_client_flags_without_common_parameter_binding(self):
        self.run_generate()
        quote=lambda p:"'"+str(p).replace("'","''")+"'"
        for name in ['claude','opencode']:
            driver=self.root/(name+'-driver.ps1')
            driver.write_text('function '+name+' { [Console]::WriteLine((ConvertTo-Json -InputObject @($args) -Compress)); $global:LASTEXITCODE=0 }\n& '+quote(self.root/('profiles/run-'+name+'.ps1'))+' -KeyFile '+quote(self.key)+" -p 'prompt with spaces' --tools Read\n",encoding='utf-8')
            p=subprocess.run(['pwsh','-NoProfile','-File',str(driver)],capture_output=True,text=True)
            self.assertEqual(p.returncode,0,p.stderr);args=json.loads(p.stdout)
            self.assertEqual(args[-4:],['-p','prompt with spaces','--tools','Read'])
if __name__=='__main__':unittest.main()
