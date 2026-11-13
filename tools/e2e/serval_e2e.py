"""驱动开启了自动化接口的 Serval（见 tools/e2e/run.sh），用于真机验收和截图。

    python3 tools/e2e/serval_e2e.py <工作目录> smoke     # 冒烟流程，见 smoke()
    python3 tools/e2e/serval_e2e.py <工作目录> shot x.png # 截一张当前窗口

也可以当模块用：

    from serval_e2e import Driver
    d = Driver("/path/to/work")
    d.press(key="sidebarAddMenu"); d.wait("添加主机"); d.press(label="添加主机"); d.set("addAddress", "127.0.0.1"); d.shot("form.png")

元素用两种方式定位：Key（代码里 c.Key / .Key 设置的，最稳定）和可访问性标签（按钮文字）。
协议是 MyGo 自动化插件的换行分隔 JSON，见 github.com/egoist/mygo/plugins/automation。
"""

import base64
import json
import os
import socket
import sys
import time


class Driver:
    def __init__(self, work, sock="serval.sock", timeout=10):
        self.work = os.path.abspath(work)
        # Unix socket 路径有长度上限，进入工作目录后用相对路径连接。
        os.chdir(self.work)
        deadline = time.time() + timeout
        while True:
            try:
                s = socket.socket(socket.AF_UNIX)
                s.connect(sock)
                break
            except OSError:
                if time.time() > deadline:
                    raise
                time.sleep(0.1)
        self.stream = s.makefile("rwb")
        self.next_id = 0

    def call(self, method, **params):
        self.next_id += 1
        self.stream.write(json.dumps({"id": self.next_id, "method": method, "params": params}).encode() + b"\n")
        self.stream.flush()
        reply = json.loads(self.stream.readline())
        if "error" in reply:
            raise RuntimeError(f"{method} {params}: {reply['error']}")
        return reply["result"]

    def nodes(self):
        self.call("capture")  # 窗口被遮挡时强制出一帧，否则读到的是旧界面
        return self.call("tree")["nodes"]

    def labels(self):
        return [n.get("label") for n in self.nodes() if n.get("label")]

    def has(self, text):
        return any(text in (n.get("label") or "") + (n.get("value") or "") for n in self.nodes())

    def wait(self, text, timeout=15):
        end = time.time() + timeout
        while time.time() < end:
            if self.has(text):
                return
            time.sleep(0.05)
        raise TimeoutError(f"等待 {text!r} 超时，界面上有：{self.labels()}")

    def find(self, label, role=None):
        for n in self.nodes():
            if n.get("label") == label and (role is None or n.get("role") == role):
                return n
        raise LookupError(f"没有标签为 {label!r} 的元素，界面上有：{self.labels()}")

    def press(self, key=None, label=None, role=None):
        """按下一个元素：优先用 key；用 label 时只匹配可以按的元素。"""
        if key:
            self.call("act", key=key, action="press")
            return
        for n in self.nodes():
            if n.get("label") == label and "press" in (n.get("actions") or []) and (role is None or n.get("role") == role):
                self.call("act", id=n["id"], action="press")
                return
        raise LookupError(f"没有可以按的 {label!r}，界面上有：{self.labels()}")

    def set(self, key, text):
        """设置输入框的内容（按 key）。元素是刚刚因为别的操作才出现的，先 wait 它附近的文字。"""
        self.call("act", key=key, action="setValue", text=text)

    def hover(self, label, role="button"):
        """把指针移到元素上，例如让主机行尾的“更多操作”按钮出现。"""
        b = self.find(label, role)["bounds"]
        self.call("input", events=[{"type": "pointerMove", "x": b["x"] + b["width"] / 2, "y": b["y"] + b["height"] / 2}])
        time.sleep(0.3)

    def shot(self, path):
        """截图。先等界面安静下来；有持续动画（例如连接中的呼吸效果）时直接截。"""
        time.sleep(0.4)
        try:
            self.call("idle", timeout=3000)
        except RuntimeError:
            pass
        with open(path, "wb") as f:
            f.write(base64.b64decode(self.call("capture")["png"]))

    def connect(self, host, secrets=(), timeout=20):
        """点击主机并处理途中出现的对话框，直到会话建立。

        指纹询问一律信任；密码或口令询问依次用 secrets 里的值回答。返回出现过的对话框列表，
        例如 ["指纹", "密码"]，验收时用来确认“没有多问”。会话没建立时抛出异常并带上界面文字。
        """
        self.press(label=host, role="button")
        seen, answers = [], list(secrets)
        end = time.time() + timeout
        while time.time() < end:
            ns = self.nodes()
            keys = [n.get("key") for n in ns]
            labels = [n.get("label") or "" for n in ns]
            if "status" in keys:  # 窗格标题行的状态圆点，出现即会话已建立
                return seen
            if any("信任这台主机吗" in l for l in labels):
                seen.append("指纹")
                self.press(label="信任并连接")
            elif "secret" in keys:
                if not answers:
                    raise RuntimeError(f"Serval 询问密码或口令，但没有可用的回答：{labels}")
                seen.append("密码")
                self.set("secret", answers.pop(0))
                self.press(key="authSubmit")
            time.sleep(0.2)
        raise TimeoutError(f"会话没有建立，对话框：{seen}，界面上有：{self.labels()}")


def smoke(d):
    """冒烟流程：添加密码登录的主机 → 第一次保存密码时设置主密码 → 连接 → 运行命令。"""
    password = open(os.path.join(d.work, "dev-password")).read().strip()
    port = os.environ.get("PORT", "22222")
    d.wait("连接一台主机")
    d.press(key="addFirst")
    d.wait("认证方式")
    for key, value in [("addAddress", "127.0.0.1"), ("addPort", port), ("addUser", os.environ.get("USER", "tester")), ("addName", "smoke")]:
        d.set(key, value)
    d.press(label="密码", role="radio")
    d.wait("只用密码登录。")  # 界面变化后先等新的一帧，新出现的元素才能按 key 找到
    d.set("addPassword", password)
    d.press(key="addSubmit")
    d.wait("设置主密码")
    d.set("setupPassword", "smoke master password")
    d.set("setupConfirm", "smoke master password")
    d.press(key="setupRemember")  # 不勾选“记住”，不往系统钥匙串里写东西
    d.press(key="setupSubmit")
    d.wait("smoke")
    seen = d.connect("smoke")
    assert seen == ["指纹"], f"保存了密码后只应询问指纹，实际：{seen}"
    d.shot("smoke-session.png")
    vault = open(os.path.join(d.work, "config", "vault.json")).read()
    assert password not in vault and "127.0.0.1" not in vault, "保险库文件里出现了明文"
    print("smoke 通过：", seen, "截图 smoke-session.png")


if __name__ == "__main__":
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    driver = Driver(sys.argv[1])
    if sys.argv[2] == "smoke":
        smoke(driver)
    elif sys.argv[2] == "shot":
        driver.shot(sys.argv[3])
    else:
        sys.exit(__doc__)
