import unittest
from check_boundaries import import_error


class ImportBoundaryTests(unittest.TestCase):
    def test_business_code_uses_other_modules_public_interfaces(self):
        owner = "github.com/StuHelper/stuhelper-reviews"
        other = "github.com/StuHelper/stuhelper-catalog"
        self.assertIsNone(import_error(owner, owner + "/service", other + "/api"))
        self.assertIsNotNone(import_error(owner, owner + "/service", other + "/repository"))
        self.assertIsNone(import_error(owner, owner + "/cmd/server", other + "/module"))

    def test_layers_follow_dependency_direction(self):
        owner = "github.com/StuHelper/stuhelper-reviews"
        self.assertIsNotNone(import_error(owner, owner + "/handler", owner + "/repository"))
        self.assertIsNotNone(import_error(owner, owner + "/repository", owner + "/service"))
        self.assertIsNone(import_error(owner, owner + "/service", owner + "/repository"))
        self.assertIsNotNone(import_error(owner, owner + "/service", owner + "/handler"))
