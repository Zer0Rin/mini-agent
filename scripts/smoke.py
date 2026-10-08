#!/usr/bin/env python3
"""SQLite + real Go server + production Next BFF + local streaming model.
Run after npm run build. --serve keeps this isolated stack for browser checks.
"""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
BASE = 'http://127.0.0.1:13000'


class Model(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        messages = body['messages']
        user = next(m['content'] for m in reversed(messages) if m['role'] == 'user')
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()

        def chunk(delta):
            data = json.dumps({'choices': [{'delta': delta}]}, ensure_ascii=False)
            raw = ('data: ' + data + '\n\n').encode()
            # Deliberately split UTF-8 characters across TCP writes.
            for pos in range(0, len(raw), 7):
                self.wfile.write(raw[pos:pos+7])
                self.wfile.flush()

        try:
            chunk({'reasoning_content': '先核对信息，再回答。'})
            if '慢' in user:
                for _ in range(60):
                    time.sleep(.25)
                    chunk({'reasoning_content': '思考中。'})
            if messages[-1]['role'] != 'tool':
                chunk({'tool_calls': [{'index': 0, 'id': 'clock-' + str(time.time_ns()), 'type': 'function',
                                      'function': {'name': 'datetime', 'arguments': '{"tz":"Asia/Taipei"}'}}]})
            else:
                for text in ['## 已完成\n\n', '中文流式回答：', '**你好，世界**。\n\n', '- 工具调用成功\n- 消息已保存\n\n', '```go\nfmt.Println("hello")\n```']:
                    chunk({'content': text})
                    time.sleep(.12)
            self.wfile.write(b'data: [DONE]\n\n')
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass


def request(path, method='GET', data=None):
    req = Request(BASE + path, data=None if data is None else json.dumps(data).encode(), method=method,
                  headers={'Content-Type': 'application/json'})
    with urlopen(req, timeout=30) as res:
        raw = res.read().decode()
        return json.loads(raw) if raw and not raw.startswith('event:') else raw


def ready(url):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            with urlopen(url, timeout=1) as res:
                if res.status == 200:
                    return
        except (URLError, TimeoutError):
            time.sleep(.2)
    raise RuntimeError('Service not ready: ' + url)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--serve', action='store_true')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='mini-agent-smoke-') as temp:
        temp = Path(temp)
        subprocess.run(['go', 'build', '-o', str(temp/'server'), './cmd/server'], cwd=ROOT, check=True)
        model = ThreadingHTTPServer(('127.0.0.1', 18081), Model)
        threading.Thread(target=model.serve_forever, daemon=True).start()
        env = {**os.environ, 'DEEPSEEK_API_KEY': 'local-smoke-placeholder', 'DEEPSEEK_BASE_URL': 'http://127.0.0.1:18081',
               'MINI_AGENT_STORE': 'sqlite', 'MINI_AGENT_DB': str(temp/'test.db'),
               'MINI_AGENT_ADDR': '127.0.0.1:18080', 'MINI_AGENT_TOKEN': 'local-smoke-token',
               'BACKEND_URL': 'http://127.0.0.1:18080', 'NEXT_TELEMETRY_DISABLED': '1'}
        children = []
        def backend():
            child = subprocess.Popen([str(temp/'server')], cwd=ROOT, env=env)
            children.append(child)
            ready('http://127.0.0.1:18080/healthz')
            return child
        def stop(child):
            child.send_signal(signal.SIGTERM)
            try:
                child.wait(timeout=12)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
        try:
            server = backend()
            web = subprocess.Popen(['node', 'node_modules/next/dist/bin/next', 'start', '-H', '127.0.0.1', '-p', '13000'], cwd=ROOT/'web', env=env)
            children.append(web)
            ready(BASE + '/api/sessions')
            assert request('/api/sessions') == []
            # Browser sends no bearer token; BFF injects it server-side.
            try:
                urlopen('http://127.0.0.1:18080/api/sessions')
                raise AssertionError('Backend should require token')
            except HTTPError as error:
                assert error.code == 401
            session = request('/api/sessions', 'POST', {'title': '冒烟会话'})
            path = '/api/sessions/' + session['id']
            raw = request(path + '/messages', 'POST', {'content': '你好，请查询时间'})
            events = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith('data: ')]
            assert events[-1]['type'] == 'done'
            assert any(e['type'] == 'tool_execution_end' for e in events)
            detail = request(path)
            assert [m['role'] for m in detail['messages']] == ['user', 'assistant', 'toolResult', 'assistant']
            assert '你好，世界' in detail['messages'][-1]['content']
            head = detail['session']['head_id']
            stop(server)
            server = backend()
            assert request(path)['session']['head_id'] == head
            request(path + '/head', 'POST', {'message_id': ''})
            request(path + '/messages', 'POST', {'content': '编辑后重新提问'})
            assert request(path)['messages'][0]['content'] == '编辑后重新提问'
            request(path, 'PATCH', {'title': '已改名'})
            assert request(path)['session']['title'] == '已改名'
            request(path, 'DELETE')
            assert request('/api/sessions') == []
            print('PASS: BFF auth, fragmented Chinese SSE, tools, done-last, SQLite persistence/restart, branching, rename/delete', flush=True)
            if args.serve:
                print('Browser check: http://127.0.0.1:13000 (local fake model; temporary database)', flush=True)
                while True:
                    time.sleep(1)
        except KeyboardInterrupt:
            pass
        finally:
            for child in reversed(children):
                if child.poll() is None:
                    stop(child)
            model.shutdown()
            model.server_close()


if __name__ == '__main__':
    main()
