CREATE OR REPLACE TRIGGER n2_accounts_biu
BEFORE UPDATE ON n2_accounts
FOR EACH ROW
BEGIN
  :NEW.balance := NVL(:OLD.balance, 0) + 1;
END;
/
