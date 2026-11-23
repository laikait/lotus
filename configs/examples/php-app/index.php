<?php
// A tiny front controller.
echo "LOTUS + PHP ", PHP_VERSION, "
";
echo "URI: ", $_SERVER["REQUEST_URI"], "
";
echo "Client: ", $_SERVER["REMOTE_ADDR"], "
";
