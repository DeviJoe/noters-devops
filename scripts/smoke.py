#!/usr/bin/env python3
"""Check local authentication and private notes through Nginx."""

import json
import os
import uuid
from http.cookiejar import CookieJar
from urllib.error import HTTPError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPCookieProcessor, Request, build_opener

BASE_URL = os.environ.get("NOTERS_URL", "http://127.0.0.1:8080").rstrip("/")
_url = urlsplit(BASE_URL)
ORIGIN = f"{_url.scheme}://{_url.netloc}"


class Client:
    def __init__(self):
        self.cookies = CookieJar()
        self.opener = build_opener(HTTPCookieProcessor(self.cookies))

    def request(self, method, path, payload=None, expected=200, csrf=True, headers=None):
        data = None if payload is None else json.dumps(payload, ensure_ascii=False).encode()
        req = Request(BASE_URL + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if csrf:
            req.add_header("X-Noters-CSRF", "1")
        if method not in ("GET", "HEAD"):
            req.add_header("Origin", ORIGIN)
        for name, value in (headers or {}).items():
            req.add_header(name, value)
        try:
            response = self.opener.open(req, timeout=10)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            assert response.status == expected, (method, path, response.status, raw)
            return json.loads(raw) if raw else None

    def token(self):
        return next(cookie.value for cookie in self.cookies if cookie.name == "noters_session")


def main():
    disposable = os.environ.get("NOTERS_SMOKE_REGISTER", "false").lower() == "true"
    token = uuid.uuid4().hex
    if disposable:
        accounts = [
            {"username": "smoke_a_" + token[:20], "password": "Smoke_" + uuid.uuid4().hex},
            {"username": "smoke_b_" + token[:20], "password": "Smoke_" + uuid.uuid4().hex},
        ]
    else:
        accounts = [
            {"username": os.environ.get("NOTERS_USERNAME"), "password": os.environ.get("NOTERS_PASSWORD")},
            {"username": os.environ.get("NOTERS_OTHER_USERNAME"), "password": os.environ.get("NOTERS_OTHER_PASSWORD")},
        ]
        if not all(account["username"] and account["password"] for account in accounts):
            raise SystemExit("Set NOTERS_USERNAME/PASSWORD and NOTERS_OTHER_USERNAME/PASSWORD; "
                             "or use NOTERS_SMOKE_REGISTER=true on a disposable test stack.")
        if accounts[0]["username"].strip().lower() == accounts[1]["username"].strip().lower():
            raise SystemExit("The smoke test requires two different accounts.")

    anonymous, owner, other = Client(), Client(), Client()
    note_id = None
    try:
        assert anonymous.request("GET", "/readyz")["status"] == "ready"
        anonymous.request("GET", "/api/notes", expected=401)
        anonymous.request("POST", "/api/notes", {"title": "anonymous"}, expected=401)
        for client, account in zip((owner, other), accounts):
            response = client.request("POST", "/api/auth/register" if disposable else "/api/auth/login",
                                      account, expected=201 if disposable else 200)
            assert response["user"]["username"] == account["username"].strip().lower()
            assert "password_hash" not in response["user"]
            assert client.request("GET", "/api/auth/me")["user"]["id"] == response["user"]["id"]
        if disposable:
            anonymous.request("POST", "/api/auth/register", accounts[0], expected=409)
        bad = {**accounts[0], "password": "incorrect password"}
        wrong = anonymous.request("POST", "/api/auth/login", bad, expected=401)
        unknown = anonymous.request("POST", "/api/auth/login", {**bad, "username": "unknown_" + token[:20]}, expected=401)
        assert wrong == unknown
        owner.request("POST", "/api/notes", {"title": "CSRF"}, expected=403, csrf=False)
        owner.request("POST", "/api/notes", {"title": "CSRF"}, expected=403,
                      headers={"Origin": "https://attacker.example"})
        owner.request("POST", "/api/notes", {"title": " "}, expected=400)
        owner.request("POST", "/api/notes", {"title": "injection", "user_id": 999}, expected=400)
        owner.request("GET", "/api/notes?limit=0", expected=400)
        note = owner.request("POST", "/api/notes", {
            "title": "  Проверка 🌱 " + token + "  ",
            "content": "Личный блокнот\n<script>alert(1)</script>\n100%_готово",
            "pinned": False,
        }, expected=201)
        note_id = note["id"]
        assert note["title"] == "Проверка 🌱 " + token
        assert owner.request("GET", f"/api/notes/{note_id}") == note
        result = owner.request("GET", "/api/notes?" + urlencode({"q": token, "limit": 1}))
        assert result["total"] == 1 and result["notes"][0]["id"] == note_id
        empty_page = owner.request("GET", "/api/notes?" + urlencode({"q": token, "offset": 1}))
        assert empty_page["total"] == 1 and empty_page["notes"] == []
        foreign_list = other.request("GET", "/api/notes?" + urlencode({"q": token}))
        assert foreign_list["total"] == 0 and foreign_list["notes"] == []
        other.request("GET", f"/api/notes/{note_id}", expected=404)
        other.request("PUT", f"/api/notes/{note_id}", {"title": " чужая "}, expected=404)
        other.request("DELETE", f"/api/notes/{note_id}", expected=404)
        assert owner.request("GET", f"/api/notes/{note_id}") == note
        updated = owner.request("PUT", f"/api/notes/{note_id}", {
            "title": "Обновлено " + token, "content": "Изменено 100%_готово", "pinned": True,
        })
        assert updated["pinned"] and updated["created_at"] == note["created_at"]
        assert updated["updated_at"] >= note["updated_at"]
        result = owner.request("GET", "/api/notes?" + urlencode({"q": "100%_готово"}))
        assert any(item["id"] == note_id for item in result["notes"])
        old_token = owner.token()
        owner.request("POST", "/api/auth/logout", expected=204)
        anonymous.request("GET", "/api/notes", expected=401,
                          headers={"Cookie": "noters_session=" + old_token})
        owner.request("POST", "/api/auth/login", accounts[0])
        assert owner.token() != old_token
        assert owner.request("GET", f"/api/notes/{note_id}") == updated
        owner.request("DELETE", f"/api/notes/{note_id}", expected=204)
        owner.request("GET", f"/api/notes/{note_id}", expected=404)
        owner.request("DELETE", f"/api/notes/{note_id}", expected=404)
        note_id = None
        print("OK: authentication, session revocation, CSRF, user isolation, CRUD, Unicode, search and pagination")
    finally:
        if note_id is not None:
            try:
                owner.request("DELETE", f"/api/notes/{note_id}", expected=204)
            except Exception as error:
                print(f"Could not clean up test note {note_id}: {error}")
        for client in (owner, other):
            try:
                client.request("POST", "/api/auth/logout", expected=204)
            except Exception:
                pass


if __name__ == "__main__":
    main()
