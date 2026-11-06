CREATE PACKAGE BODY hr.payroll AS
--                     ^^^^^^^ definition scip-plsql oracle demo 1 HR/PAYROLL#
  PROCEDURE raise_all(p_value NUMBER) IS
--          ^^^^^^^^^ definition scip-plsql oracle demo 1 HR/PAYROLL#RAISE_ALL().
--                    ^^^^^^^ definition scip-plsql oracle demo 1 HR/PAYROLL#RAISE_ALL().(P_VALUE)
  BEGIN
    UPDATE employees SET salary = salary + p_value;
--         ^^^^^^^^^ reference scip-plsql oracle demo 1 HR/EMPLOYEES#
--                       ^^^^^^ reference scip-plsql oracle demo 1 HR/EMPLOYEES#SALARY.
--                                ^^^^^^ reference scip-plsql oracle demo 1 HR/EMPLOYEES#SALARY.
--                                         ^^^^^^^ reference scip-plsql oracle demo 1 HR/PAYROLL#RAISE_ALL().(P_VALUE)
  END;
END;
/
