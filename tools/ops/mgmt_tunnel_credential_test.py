import io
import unittest
from unittest import mock

import mgmt_tunnel_credential as tunnel


class TransportCredentialTest(unittest.TestCase):
    credential = {"client_id": "fixture-client", "client_secret": "fixture-secret",
                  "expires_at": "2099-01-01T00:00:00Z"}

    def test_expired_or_ambiguous_credentials_fail_before_keychain_write(self):
        for expiry in ("2000-01-01T00:00:00Z", "2099-01-01T00:00:00", "invalid"):
            with self.subTest(expiry=expiry), mock.patch.object(tunnel, "keychain") as keychain:
                with self.assertRaises(ValueError):
                    tunnel.store(dict(self.credential, expires_at=expiry))
                keychain.assert_not_called()

    def test_transport_secret_is_in_environment_only_and_listener_is_loopback(self):
        for host, port in (("k8s-operator.guardianintelligence.org", "16443"),
                           ("talos-operator.guardianintelligence.org", "50000")):
            with mock.patch.object(tunnel.sys, "platform", "darwin"), \
                    mock.patch.object(tunnel.sys, "argv", ["wrapper", "run", "/cloudflared", host, port]), \
                    mock.patch.object(tunnel, "load", return_value=self.credential), \
                    mock.patch.object(tunnel.os, "execve") as execute:
                tunnel.main()
                binary, argv, env = execute.call_args.args
                self.assertEqual(binary, "/cloudflared")
                self.assertIn("127.0.0.1:" + port, argv)
                self.assertNotIn(self.credential["client_secret"], argv)
                self.assertEqual(env["TUNNEL_SERVICE_TOKEN_SECRET"], self.credential["client_secret"])

    def test_ambient_cloud_provider_tokens_are_not_used_as_operator_credentials(self):
        with mock.patch.object(tunnel.sys, "platform", "darwin"), \
                mock.patch.object(tunnel.sys, "argv", ["wrapper", "run", "/cloudflared",
                    "k8s-operator.guardianintelligence.org", "16443"]), \
                mock.patch.object(tunnel, "load", return_value=None), \
                mock.patch.dict(tunnel.os.environ, {"TUNNEL_SERVICE_TOKEN_ID": "unrelated",
                    "TUNNEL_SERVICE_TOKEN_SECRET": "unrelated"}), \
                mock.patch.object(tunnel.os, "execve") as execute:
            tunnel.main()
            env = execute.call_args.args[2]
            self.assertNotIn("TUNNEL_SERVICE_TOKEN_ID", env)
            self.assertNotIn("TUNNEL_SERVICE_TOKEN_SECRET", env)

    def test_other_destinations_never_receive_the_credential(self):
        for host, port in (("example.com", "16443"),
                           ("k8s-operator.guardianintelligence.org", "50000")):
            with mock.patch.object(tunnel.sys, "platform", "darwin"), \
                    mock.patch.object(tunnel.sys, "argv", ["wrapper", "run", "/cloudflared", host, port]), \
                    mock.patch.object(tunnel, "load") as load:
                with self.assertRaises(ValueError):
                    tunnel.main()
                load.assert_not_called()

    def test_status_does_not_print_the_transport_secret(self):
        with mock.patch.object(tunnel.sys, "platform", "darwin"), \
                mock.patch.object(tunnel.sys, "argv", ["wrapper", "status"]), \
                mock.patch.object(tunnel, "load", return_value=self.credential), \
                mock.patch.object(tunnel.sys, "stdout", new_callable=io.StringIO) as output:
            tunnel.main()
            self.assertIn(self.credential["expires_at"], output.getvalue())
            self.assertNotIn(self.credential["client_secret"], output.getvalue())


if __name__ == "__main__":
    unittest.main()
